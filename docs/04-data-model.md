# 4. Data model + lựa chọn DB

## 4.1 Chọn DB

| Phương án | Ưu | Nhược | Quyết định |
|---|---|---|---|
| **PostgreSQL + JSONB** | schema form động **mà vẫn giữ transaction** → `submission + consent_record` atomic; RLS cho multi-tenant; partitioning cho event; `SKIP LOCKED` làm queue; GIN index để filter answers | phải kỷ luật với JSONB (đánh index có chọn lọc) | ✅ **Chọn** |
| MongoDB | schema động tự nhiên hơn | không có cách đơn giản để đảm bảo "consent và data cùng sống hoặc cùng chết"; thêm một hệ vận hành; không có RLS | ❌ |
| Postgres + ClickHouse cho analytics | funnel query cực nhanh | thêm container + ETL, cho 340k event/ngày là thừa | ❌ — [scaling path](07-operations.md#74-scaling-path) |

> Lý do quyết định không phải "JSONB tiện" mà là **ràng buộc pháp lý cần transaction**. Xem [6.3](06-deep-dives.md#63--consent--data-subject-rights).

## 4.2 Multi-tenancy

**Shared DB + `tenant_id` trên mọi bảng + Row-Level Security.**

```sql
ALTER TABLE forms.submissions ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON forms.submissions
  USING (tenant_id = current_setting('app.tenant_id')::uuid);
```
App chạy `SET LOCAL app.tenant_id = $1` ở đầu mỗi transaction.

RLS là **lưới an toàn cuối cùng** chống lỗi lập trình quên `WHERE tenant_id`. Với dữ liệu cá nhân, rò rỉ chéo tenant không phải bug thường — nó là sự cố phải báo cáo cơ quan chức năng. Chi phí RLS (~3-5% overhead) là rẻ so với rủi ro đó.

| Phương án | Đánh giá |
|---|---|
| **Shared DB + RLS** | ✅ Chọn. 200 tenant / 80 GB, backup–migrate–vận hành đều đơn giản |
| Schema per tenant | ❌ 200 schema × N bảng = migration khổ sở, connection pool phân mảnh |
| DB per tenant | ❌ Trái hoàn toàn với mục tiêu "một lệnh docker-compose" |

## 4.3 Phân tách schema Postgres theo module

```
iam.*        workspaces, users, memberships, api_tokens
links.*      domains, links
forms.*      forms, form_versions, submissions, submission_revisions
files.*      files
consent.*    purposes, documents, data_subjects, records, current_consents, dsr_requests
analytics.*  events (partitioned), funnel_rollups
audit.*      entries
core.*       idempotency_keys, outbox, jobs
```

Không chỉ là quy ước đặt tên: đây là **ranh giới bounded context thực thi được**. Mỗi module có DB role riêng với `search_path` giới hạn; `audit` role chỉ có `INSERT` + `SELECT`. Đồng thời là đường cắt sẵn nếu sau này tách service.

---

## 4.4 Links

```sql
CREATE TABLE links.domains (
  id UUID PRIMARY KEY, tenant_id UUID NOT NULL,
  host TEXT UNIQUE NOT NULL, is_default BOOLEAN DEFAULT false
);

CREATE TABLE links.links (
  id          UUID PRIMARY KEY,
  tenant_id   UUID NOT NULL,
  domain_id   UUID NOT NULL REFERENCES links.domains(id),
  code        TEXT NOT NULL,                    -- 7 ký tự base62 random, hoặc custom alias
  target_url  TEXT,                             -- NULL nếu link trỏ tới form nội bộ
  form_id     UUID REFERENCES forms.forms(id),
  expires_at  TIMESTAMPTZ,
  status      TEXT NOT NULL DEFAULT 'active',   -- active | disabled | deleted | legal_hold
  created_by  UUID,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (target_url IS NOT NULL OR form_id IS NOT NULL)
);

CREATE UNIQUE INDEX links_lookup ON links.links (domain_id, lower(code));  -- 🔥 truy vấn hot path DUY NHẤT
CREATE INDEX ON links.links (tenant_id, created_at DESC);
CREATE INDEX ON links.links (expires_at) WHERE status = 'active' AND expires_at IS NOT NULL;
```

### Sinh short code

Lệch khỏi mặc định "counter → base62", có lý do định lượng:

| Cách | Ưu | Nhược | Chọn |
|---|---|---|---|
| Counter / Snowflake → base62 | 0 va chạm by construction | **liệt kê được**: crawl `aaaa1…aaaaZ` để lấy toàn bộ link → suy ra khối lượng kinh doanh của tenant, và mỗi link là cửa vào một form thu thập dữ liệu cá nhân | ❌ |
| **Random 7 ký tự base62 + UNIQUE + retry ≤ 3** | không đoán được | va chạm về lý thuyết | ✅ |

Toán: keyspace 62⁷ ≈ 3,5 × 10¹². Ở 500k link/domain, xác suất va chạm mỗi lần insert ≈ **1,4 × 10⁻⁷** → khoảng 1 lần retry mỗi 7 triệu link. Retry 3 lần quá đủ.

**Luôn dựa vào `UNIQUE` constraint làm trọng tài, không bao giờ SELECT-then-INSERT** (race condition).

**Custom alias:** cùng bảng, cùng unique index; validate `^[a-zA-Z0-9_-]{3,64}$` + blacklist (`api`, `admin`, `r`, `f`, `q`, `dsr`, `health`, `_next`, …).

**Không dedupe theo `target_url`** — cùng một URL cần nhiều link để có expiry riêng, analytics riêng, alias riêng. Quyết định có chủ đích.

---

## 4.5 Forms + versioning

```sql
CREATE TABLE forms.forms (
  id UUID PRIMARY KEY, tenant_id UUID NOT NULL,
  public_id TEXT UNIQUE NOT NULL,
  title TEXT NOT NULL,
  live_version_id UUID,                        -- version đang phục vụ public
  draft_schema JSONB,                          -- bản nháp, ghi đè thoải mái
  status TEXT NOT NULL DEFAULT 'draft',        -- draft | live | closed | archived
  -- chính sách tuân thủ gắn với form:
  retention_days   INT,                        -- NULL = theo mặc định workspace
  retention_action TEXT NOT NULL DEFAULT 'delete',   -- delete | anonymize
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE forms.form_versions (
  id UUID PRIMARY KEY, tenant_id UUID NOT NULL,
  form_id UUID NOT NULL REFERENCES forms.forms(id),
  version_no INT NOT NULL,
  schema JSONB NOT NULL,                       -- BẤT BIẾN sau khi publish
  schema_hash BYTEA NOT NULL,
  consent_document_id UUID,                    -- văn bản đồng ý gắn cứng vào version
  published_at TIMESTAMPTZ NOT NULL,
  published_by UUID NOT NULL,
  retired_at TIMESTAMPTZ,                      -- chỉ dùng khi phải gỡ khẩn cấp
  UNIQUE (form_id, version_no)
);
```

> **Bất biến sau publish** là nền tảng của cả tính năng versioning lẫn bằng chứng pháp lý. Khi có tranh chấp "khách hàng đã đồng ý cái gì", ta tái dựng **chính xác trang họ đã nhìn thấy** từ `form_versions.schema` + `consent.documents.body_html`.

### Cấu trúc `schema` JSONB

```jsonc
{
  "v": 1,                                   // schema-of-schema version
  "pages": [
    {"id": "pg_a1", "title": "Thông tin liên hệ", "fields": ["fld_name","fld_phone","fld_used"]},
    {"id": "pg_b2", "title": "Trải nghiệm", "next": "pg_end",
     "fields": ["fld_rating","fld_note"]},   // next = điều hướng mặc định của trang
    {"id": "pg_end","title": "Hoàn tất",          "fields": []}
  ],
  "fields": {
    "fld_name":   {"type":"text","label":"Họ và tên","required":true,"pii":"name"},
    "fld_phone":  {"type":"text","label":"Số điện thoại","required":true,
                   "pii":"phone","identifier":true},        // dùng để nhận diện chủ thể cho DSR
    "fld_health": {"type":"text","label":"Tình trạng sức khoẻ","sensitive":true},
    "fld_used":   {"type":"choice","label":"Bạn đã dùng sản phẩm chưa?",
                   "options":[{"id":"opt_yes","label":"Rồi"},{"id":"opt_no","label":"Chưa"}]},
    "fld_tags":   {"type":"multi_choice","options":[{"id":"opt_a","label":"A"},{"id":"opt_c","label":"C"}]},
    "fld_rating": {"type":"rating","scale":5},
    "fld_when":   {"type":"date","min":"2020-01-01"},
    "fld_city":   {"type":"dropdown","options":[…]},
    "fld_cv":     {"type":"file","accept":["application/pdf"],"max_mb":10},
    "fld_note":   {"type":"text","multiline":true}
  },
  "rules": [
    {"id":"rl_1","on_page":"pg_a1",
     "when": {"op":"eq","field":"fld_used","value":"opt_yes"},
     "then": [{"action":"goto","target":"pg_b2"}],
     "else": [{"action":"goto","target":"pg_end"}]},
    {"id":"rl_2","on_page":"pg_b2",
     "when": {"op":"lte","field":"fld_rating","value":2},
     "then": [{"action":"show","target":"fld_note"},{"action":"require","target":"fld_note"}]}
  ],
  "consent": {
    "purposes": [{"code":"service","required":true},{"code":"marketing","required":false}],
    "sensitive_notice_required": true          // tự bật vì tồn tại field sensitive:true
  },
  "limits": {"max_fields": 200, "max_rules": 300}
}
```

### Ba bất biến khiến version sống chung được với dữ liệu cũ

1. **`field_id` / `option_id` là ULID ổn định, không bao giờ tái sử dụng.** Đổi label không sinh id mới.
2. **Câu trả lời lưu id, không lưu label.** `{"fld_used": "opt_yes"}`, không phải `{"Bạn đã dùng?": "Rồi"}`.
3. **Version đã publish không bao giờ bị sửa.** Mọi thay đổi = version mới.

Chi tiết ma trận tương thích và cách grid hiển thị: [6.2](06-deep-dives.md#62--schema-versioning-sống-chung-với-conditional-logic).

---

## 4.6 Submissions

```sql
CREATE TABLE forms.submissions (
  id UUID PRIMARY KEY, tenant_id UUID NOT NULL,
  form_id         UUID NOT NULL,
  form_version_id UUID NOT NULL,              -- pin: bản ghi luôn tự diễn giải được
  data_subject_id UUID,                       -- suy từ field identifier:true
  answers         JSONB NOT NULL,             -- {field_id: value} — RAW, không transform lúc nhận
  answers_enc     BYTEA,                      -- các field sensitive:true, AES-256-GCM bằng DEK của chủ thể
  visible_fields  TEXT[] NOT NULL,            -- tập field THỰC SỰ hiển thị, do server tự tính
  visit_id        UUID,                       -- nối funnel
  meta            JSONB,                      -- {ip_prefix:"1.2.3.0/24", country, ua_family} — KHÔNG lưu IP đầy đủ
  status          TEXT NOT NULL DEFAULT 'active',  -- active | restricted | erased
  submitted_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  purge_at        TIMESTAMPTZ                 -- tính từ retention policy tại thời điểm submit
);

CREATE INDEX ON forms.submissions (tenant_id, form_id, submitted_at DESC);        -- grid
CREATE INDEX ON forms.submissions (data_subject_id) WHERE data_subject_id IS NOT NULL;  -- DSR
CREATE INDEX ON forms.submissions (purge_at) WHERE status = 'active';             -- retention sweeper
CREATE INDEX ON forms.submissions USING GIN (answers jsonb_path_ops);             -- filter trong grid

CREATE TABLE forms.submission_revisions (     -- chủ thể sửa dữ liệu → giữ vết, KHÔNG ghi đè
  id UUID PRIMARY KEY, tenant_id UUID NOT NULL,
  submission_id UUID NOT NULL,
  answers_before JSONB NOT NULL,
  changed_by TEXT NOT NULL,                   -- 'subject:<id>' | 'user:<id>'
  change_source TEXT NOT NULL,                -- dsr_self_service | admin_edit
  changed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

**`visible_fields` là chi tiết nhỏ nhưng cứu grid.** Nó cho phép phân biệt ba trạng thái ô:

| Điều kiện | Grid hiển thị | Nghĩa |
|---|---|---|
| `field_id ∉ schema(version của bản ghi)` | `—` | Không hỏi ở version này |
| `field_id ∈ schema` nhưng `∉ visible_fields` | `∅` | Bị ẩn theo nhánh rẽ |
| `∈ visible_fields`, answer rỗng | `""` | Có hỏi, người dùng bỏ trống |

Thiếu nó thì cả ba trông giống nhau và mọi thống kê về tỉ lệ bỏ trống trở nên vô nghĩa.

---

## 4.7 Files

```sql
CREATE TABLE files.files (
  id UUID PRIMARY KEY, tenant_id UUID NOT NULL,
  storage_key   TEXT UNIQUE NOT NULL,         -- <tenant>/<yyyy>/<mm>/<ulid>  — cùng contract cho local & s3
  original_name TEXT NOT NULL,
  content_type  TEXT NOT NULL,                -- xác định bằng MAGIC BYTES, không tin client
  size_bytes    BIGINT NOT NULL,
  checksum      BYTEA NOT NULL,               -- sha256
  encrypted     BOOLEAN NOT NULL DEFAULT false,
  data_subject_id UUID,                       -- để crypto-shred khi xóa
  submission_id UUID,                         -- NULL = orphan chờ dọn
  status        TEXT NOT NULL DEFAULT 'pending',  -- pending | bound | erased
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX ON files.files (status, created_at) WHERE status = 'pending';   -- orphan sweeper
CREATE INDEX ON files.files (data_subject_id) WHERE data_subject_id IS NOT NULL;
```
Metadata trong DB, byte ngoài DB. **Không bao giờ lưu bytea/BLOB** (dump phình, restore hàng giờ).

---

## 4.8 Consent — bounded context riêng

```sql
CREATE TABLE consent.purposes (              -- "mục đích xử lý" theo luật
  id UUID PRIMARY KEY, tenant_id UUID NOT NULL,
  code TEXT NOT NULL, name TEXT NOT NULL, description TEXT NOT NULL,
  legal_basis TEXT NOT NULL,                 -- consent | contract | legal_obligation | vital_interest
  retention_days INT,
  is_required BOOLEAN NOT NULL DEFAULT false,
  UNIQUE (tenant_id, code)
);

CREATE TABLE consent.documents (             -- văn bản thông báo / điều khoản, có version
  id UUID PRIMARY KEY, tenant_id UUID NOT NULL,
  kind TEXT NOT NULL,                        -- privacy_notice | consent_text
  version_no INT NOT NULL,
  body_html TEXT NOT NULL,
  content_hash BYTEA NOT NULL,
  effective_from TIMESTAMPTZ NOT NULL,
  created_by UUID NOT NULL,
  UNIQUE (tenant_id, kind, version_no)       -- BẤT BIẾN sau publish
);

CREATE TABLE consent.data_subjects (
  id UUID PRIMARY KEY, tenant_id UUID NOT NULL,
  identifier_hash BYTEA NOT NULL,            -- HMAC-SHA256(normalize(email|phone), tenant_pepper)
  identifier_kind TEXT NOT NULL,             -- email | phone
  dek_wrapped BYTEA,                         -- khóa mã hóa riêng của chủ thể, bọc bởi tenant KEK
  erased_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, identifier_kind, identifier_hash)
);

CREATE TABLE consent.records (               -- APPEND-ONLY. Rút đồng ý = THÊM dòng, không sửa dòng cũ.
  id UUID PRIMARY KEY, tenant_id UUID NOT NULL,
  data_subject_id UUID NOT NULL,
  purpose_id      UUID NOT NULL,
  submission_id   UUID,
  form_version_id UUID,
  action          TEXT NOT NULL,             -- granted | withdrawn
  document_id     UUID NOT NULL,             -- ĐÚNG văn bản họ đã nhìn thấy
  evidence        JSONB NOT NULL,
  occurred_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX ON consent.records (tenant_id, data_subject_id, purpose_id, occurred_at DESC);

-- Bảng phái sinh cho truy vấn nhanh; consent.records LUÔN là nguồn sự thật.
CREATE TABLE consent.current_consents (
  tenant_id UUID NOT NULL, data_subject_id UUID NOT NULL, purpose_id UUID NOT NULL,
  granted BOOLEAN NOT NULL, last_record_id UUID NOT NULL, updated_at TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (tenant_id, data_subject_id, purpose_id)
);
```

`evidence` chứa đủ để tái dựng bằng chứng:
```json
{
  "rendered_hash": "sha256:9a1f…",     // SHA-256 của đúng HTML đã render cho họ
  "checkbox_state": {"service": true, "marketing": false},
  "method": "checkbox",
  "ip_prefix": "1.2.3.0/24",
  "user_agent": "Mozilla/5.0 …",
  "locale": "vi-VN",
  "ts_client": "2026-08-06T10:00:00+07:00",
  "ts_server": "2026-08-06T03:00:01Z"
}
```

## 4.9 DSR requests

```sql
CREATE TABLE consent.dsr_requests (
  id UUID PRIMARY KEY, tenant_id UUID NOT NULL,
  data_subject_id UUID NOT NULL,
  type   TEXT NOT NULL,                      -- access|rectify|erase|restrict|withdraw|export|object
  status TEXT NOT NULL DEFAULT 'received',   -- received→verified→in_progress→fulfilled|rejected
  verification_method TEXT,                  -- email_otp | sms_otp | receipt_token
  received_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  due_at       TIMESTAMPTZ NOT NULL,         -- received_at + DSR_SLA_HOURS (cấu hình, mặc định 72h)
  fulfilled_at TIMESTAMPTZ,
  handled_by   UUID,
  resolution_note TEXT,
  artifact_key TEXT                          -- file export kết quả, nếu có
);
CREATE INDEX ON consent.dsr_requests (tenant_id, status, due_at) WHERE status <> 'fulfilled';
```
Index partial này chính là thứ nuôi metric nghiệp vụ quan trọng nhất của hệ thống: **số DSR quá hạn**.

## 4.10 Audit log bất biến

```sql
CREATE TABLE audit.entries (
  tenant_id UUID NOT NULL,
  seq       BIGINT NOT NULL,                 -- tăng đơn điệu theo tenant
  actor     JSONB NOT NULL,                  -- {type: user|subject|system, id, ip_prefix}
  action    TEXT NOT NULL,                   -- submission.read_bulk | consent.withdrawn | dsr.fulfilled …
  target    JSONB NOT NULL,
  payload   JSONB,
  prev_hash BYTEA NOT NULL,
  hash      BYTEA NOT NULL,                  -- sha256(prev_hash ‖ canonical_json(row))
  occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, seq)
);
REVOKE UPDATE, DELETE ON audit.entries FROM collectr_app;   -- DB role chỉ INSERT + SELECT
```

Hash chain làm log **tamper-evident**: sửa hoặc xóa một dòng làm gãy chuỗi. Job hằng ngày ký checkpoint `(tenant, max_seq, hash)` bằng khóa riêng và lưu ra ngoài DB.

**Sự kiện bắt buộc ghi:** submission created/updated/erased · consent granted/withdrawn · **submission.read_bulk (export)** · dsr received/fulfilled · form version published · retention purge · sensitive field revealed · login / permission change.

## 4.11 Analytics

```sql
CREATE TABLE analytics.events (
  id UUID NOT NULL, tenant_id UUID NOT NULL,
  event_id TEXT NOT NULL,                    -- ULID client-side → khóa idempotency
  type TEXT NOT NULL,                        -- click | form_view | form_start | submit
  link_id UUID, form_id UUID, form_version_id UUID,
  visit_id UUID, meta JSONB,
  occurred_at TIMESTAMPTZ NOT NULL
) PARTITION BY RANGE (occurred_at);          -- retention = DROP PARTITION, không DELETE hàng loạt
CREATE UNIQUE INDEX ON analytics.events (tenant_id, event_id);

CREATE TABLE analytics.funnel_rollups (
  tenant_id UUID NOT NULL, form_id UUID, link_id UUID,
  bucket TIMESTAMPTZ NOT NULL,               -- 5 phút
  clicks INT NOT NULL DEFAULT 0, views INT NOT NULL DEFAULT 0,
  starts INT NOT NULL DEFAULT 0, submits INT NOT NULL DEFAULT 0,
  PRIMARY KEY (tenant_id, form_id, link_id, bucket)
);
```

## 4.12 Idempotency & Outbox

```sql
CREATE TABLE core.idempotency_keys (
  tenant_id UUID NOT NULL, scope TEXT NOT NULL, key TEXT NOT NULL,
  request_hash BYTEA NOT NULL,               -- key giống + body KHÁC = từ chối
  status TEXT NOT NULL CHECK (status IN ('PENDING','COMPLETED','FAILED')),
  response_body JSONB,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, scope, key)        -- scope theo tenant, KHÔNG toàn cục
);

CREATE TABLE core.outbox (
  id BIGSERIAL PRIMARY KEY, tenant_id UUID NOT NULL,
  topic TEXT NOT NULL, payload JSONB NOT NULL,
  available_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  attempts INT NOT NULL DEFAULT 0,
  locked_until TIMESTAMPTZ, sent_at TIMESTAMPTZ, last_error TEXT
);
CREATE INDEX ON core.outbox (available_at) WHERE sent_at IS NULL;   -- worker: FOR UPDATE SKIP LOCKED
```
