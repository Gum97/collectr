# 10. Public API & Webhooks

Hai chiều tích hợp: **pull** (API key gọi vào) và **push** (webhook đẩy ra).

---

## 10.1 Xác thực — API key

```
Authorization: Bearer clc_live_7f3a…            # prefix cho biết môi trường, quét được trong git leak scanner
```

```sql
CREATE TABLE iam.api_keys (
  id UUID PRIMARY KEY, tenant_id UUID NOT NULL,
  project_id UUID,                          -- NULL = phạm vi toàn org
  name TEXT NOT NULL,
  prefix TEXT NOT NULL,                     -- 8 ký tự đầu, hiện trong UI để nhận diện
  key_hash BYTEA NOT NULL,                  -- sha256(key) — KHÔNG lưu key gốc
  scopes TEXT[] NOT NULL,                   -- tập con capability của người tạo
  ip_allowlist INET[],
  expires_at TIMESTAMPTZ,
  last_used_at TIMESTAMPTZ, revoked_at TIMESTAMPTZ,
  created_by UUID NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX ON iam.api_keys (prefix);
```

| Quy tắc | Lý do |
|---|---|
| Key gốc **chỉ hiện một lần** lúc tạo | Lưu được thì rò rỉ được |
| Hash bằng sha256, không argon2 | Key có 256 bit entropy → brute-force bất khả thi; argon2 sẽ thêm ~50ms vào **mọi** request |
| `scopes` là tập con capability của người tạo | Không thể tự nâng quyền bằng cách tạo key |
| Bắt buộc có `expires_at` (mặc định 1 năm) | Key vĩnh viễn là nợ kỹ thuật vĩnh viễn |
| Cảnh báo email khi key sắp hết hạn / dùng từ IP mới | |

**API key không bao giờ được cấp scope `submission.read_sensitive` hoặc `dsr.handle`** — hai việc này cần con người chịu trách nhiệm, không phải một chuỗi ký tự nằm trong file CI.

## 10.2 Quy ước chung

| Khía cạnh | Quy ước |
|---|---|
| Version | Trong path: `/api/v1/…`. Breaking change → `/api/v2`, v1 sống thêm tối thiểu 12 tháng |
| Phân trang | **Cursor**, không offset: `?cursor=…&limit=50` → `{data, next_cursor}`. Offset sai số khi có dữ liệu chèn vào giữa và chậm dần ở trang sâu |
| Sắp xếp | `?sort=-submitted_at` |
| Lọc | `?filter[fld_city]=hanoi&filter[submitted_at][gte]=2026-01-01` |
| Sparse fields | `?fields=id,submitted_at,answers.fld_name` |
| Lỗi | RFC 7807 + `trace_id` |
| Idempotency | Bắt buộc `Idempotency-Key` cho mọi `POST` có tác dụng phụ |
| Rate limit | Header `X-RateLimit-Limit/Remaining/Reset`; `429` kèm `Retry-After` |
| Nén | `Accept-Encoding: gzip` |

**Giới hạn mặc định:** 600 req/phút/key, `limit ≤ 100`, response ≤ 5 MB. Endpoint export và analytics chặt hơn (10/giờ).

## 10.3 Bề mặt API công khai

```
# Links
GET|POST      /api/v1/links
GET|PATCH|DEL /api/v1/links/{id}
POST          /api/v1/links/bulk              # ≤ 1000 link/lần, trả kết quả từng dòng
GET           /api/v1/links/{id}/stats?from=&to=

# Forms
GET           /api/v1/forms
GET           /api/v1/forms/{id}
GET           /api/v1/forms/{id}/versions/{no}/schema
GET           /api/v1/forms/{id}/submissions   # cursor, filter, sparse fields
GET           /api/v1/forms/{id}/submissions/{sid}
GET           /api/v1/forms/{id}/analytics/funnel
POST          /api/v1/forms/{id}/exports       # → job, xem doc 9

# Files
GET           /api/v1/forms/{id}/files          # tệp đính kèm đã nhận
POST          /api/v1/files/{id}/download-url   # → link ký 10 phút, ghi audit

# Consent (chỉ đọc qua API — ghi phải qua form thật để có bằng chứng)
GET           /api/v1/subjects/{id}/consents
GET           /api/v1/consent/purposes

# Webhooks
GET|POST      /api/v1/webhooks
GET|PATCH|DEL /api/v1/webhooks/{id}
GET           /api/v1/webhooks/{id}/deliveries
POST          /api/v1/webhooks/{id}/deliveries/{did}/replay
```

> **Không có `POST /api/v1/submissions`.** Tạo submission qua API nghĩa là tạo dữ liệu cá nhân không có bằng chứng đồng ý — chính thứ mà toàn bộ thiết kế này tồn tại để ngăn. Ai cần nhập liệu hàng loạt thì dùng import có khai báo căn cứ pháp lý (ngoài MVP).

### Định dạng câu trả lời

Một câu hỏi kiểu `text` có thể khai `format`. Danh sách đóng, không phải ô nhập
biểu thức chính quy: mẫu do người dùng viết sẽ chạy trong trình duyệt của người
điền form, nơi một mẫu quay lui vô hạn làm treo máy họ — và người dán mẫu đó
không bao giờ nhìn thấy hậu quả.

| `format` | Nhận | Bàn phím trên điện thoại |
|---|---|---|
| `email` | một `@`, có dấu chấm trong tên miền | `email` |
| `phone_vn` | 10 chữ số bắt đầu bằng `0`, hoặc `+84` + 9 số | `tel` |
| `tax_code` | 10 chữ số, hoặc kèm 3 số đơn vị phụ thuộc | `numeric` |
| `national_id` | đúng 12 chữ số (CMND 9 số đã hết hiệu lực) | `numeric` |
| `url` | bắt đầu bằng `http://` hoặc `https://` | `url` |
| `number` | số, chấp nhận dấu phẩy thập phân | `decimal` |
| `integer` | số nguyên | `numeric` |

Dấu cách, dấu chấm và dấu gạch ngang được bỏ trước khi so khớp cho số điện thoại,
mã số thuế và CCCD: từ chối một câu trả lời đúng chỉ vì nó có dấu ngăn cách là
mất một lượt gửi, và không có cách nào biết chuyện đó đã xảy ra.

`min` và `max` giới hạn khoảng — ngày theo dạng `YYYY-MM-DD`, số theo giá trị.
Khoảng ngược (min > max) bị chặn ngay ở bước publish, vì nó từ chối **mọi** câu
trả lời.

Máy chủ kiểm lại toàn bộ khi nhận bài gửi. Kiểm ở trình duyệt chỉ để người điền
thấy lỗi sớm; một bài gửi là một HTTP request và ai cũng tạo được.

## 10.4 Webhooks

### Cấu hình

```sql
CREATE TABLE integrations.webhooks (
  id UUID PRIMARY KEY, tenant_id UUID NOT NULL, project_id UUID NOT NULL,
  url TEXT NOT NULL,
  events TEXT[] NOT NULL,
  secret_enc BYTEA NOT NULL,                -- ký HMAC, mã hóa at rest
  active BOOLEAN NOT NULL DEFAULT true,
  include_answers BOOLEAN NOT NULL DEFAULT false,   -- MẶC ĐỊNH KHÔNG gửi dữ liệu cá nhân
  consecutive_failures INT NOT NULL DEFAULT 0,
  disabled_at TIMESTAMPTZ, disabled_reason TEXT,
  created_by UUID NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE integrations.deliveries (
  id UUID PRIMARY KEY, tenant_id UUID NOT NULL, webhook_id UUID NOT NULL,
  event_id UUID NOT NULL, event_type TEXT NOT NULL,
  payload JSONB NOT NULL,
  attempt INT NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'pending',   -- pending|delivered|failed|dead
  response_code INT, response_snippet TEXT, -- cắt còn 1KB, KHÔNG log body đầy đủ
  next_attempt_at TIMESTAMPTZ, delivered_at TIMESTAMPTZ
);
CREATE INDEX ON integrations.deliveries (status, next_attempt_at) WHERE status = 'pending';
```

### Sự kiện

`link.created` · `link.clicked` (gộp lô 10s) · `form.published` · **`submission.created`** · `submission.updated` · `consent.withdrawn` · `dsr.received` · `dsr.due_soon` · `export.ready`

`consent.withdrawn` và `dsr.received` là hai sự kiện quan trọng nhất về mặt tuân thủ — chúng cho phép hệ thống CRM phía sau dừng xử lý ngay thay vì đợi đồng bộ định kỳ.

### Payload & chữ ký

```http
POST https://khachhang.vn/hooks/collectr
X-Collectr-Event: submission.created
X-Collectr-Delivery: 018f…
X-Collectr-Timestamp: 1754467200
X-Collectr-Signature: sha256=9a1f…      # HMAC(secret, timestamp ‖ "." ‖ body)
```
```json
{
  "id": "evt_018f…",
  "type": "submission.created",
  "created_at": "2026-08-06T10:00:00Z",
  "data": {
    "submission_id": "sb_01J…",
    "form_id": "fm_01J…", "form_version": 3,
    "project_id": "pj_01J…",
    "consents": {"service": true, "marketing": false},
    "source_link": "Xk9mQ2v"
    // "answers" CHỈ có khi include_answers = true
  }
}
```

Bên nhận phải kiểm `timestamp` lệch ≤ 5 phút (chống replay) và so sánh chữ ký **constant-time**. Tài liệu sẽ ghi rõ điều này kèm ví dụ Go/PHP/Node — hầu hết lỗi tích hợp webhook nằm ở đây.

### Giao hàng

```
outbox (cùng transaction với nghiệp vụ)
  → relay worker → integrations.deliveries
  → HTTP POST, timeout 10s
  → 2xx: delivered
  → 4xx (trừ 408/429): FAIL NGAY, không retry — lỗi vĩnh viễn, retry chỉ tổ phá bên kia
  → 5xx/timeout/429: retry 8 lần, backoff mũ + jitter (10s → 30s → 2m → 10m → 30m → 2h → 6h → 12h)
  → hết lượt: status='dead', giữ 30 ngày để replay thủ công
  → 20 lần thất bại liên tiếp: TỰ TẮT webhook + email cảnh báo
```

**Jitter là bắt buộc.** Sau khi endpoint của khách hàng sập 1 giờ, hàng nghìn delivery cùng đến hạn — không có jitter thì ta tự DDoS họ đúng lúc họ vừa hồi phục.

Tự tắt sau 20 lần thất bại liên tiếp để queue không bị nghẽn vĩnh viễn bởi một endpoint đã chết từ lâu.

### Bảo vệ SSRF

Webhook là endpoint cho phép người dùng khiến **server của ta** gọi tới URL do **họ** chọn — đường vào mạng nội bộ kinh điển.

- Chỉ `https` (cho phép `http` nếu bật cờ dev).
- Resolve DNS **trước**, chặn dải riêng tư: `127/8` `10/8` `172.16/12` `192.168/16` `169.254/16` `::1` `fc00::/7`.
- **Kiểm lại IP sau mỗi redirect** (DNS rebinding), tối đa 3 redirect.
- Chạy qua HTTP client riêng, không có credential nào của hệ thống.

### Góc tuân thủ

| Vấn đề | Xử lý |
|---|---|
| Webhook = chuyển dữ liệu cho bên thứ ba | `include_answers=true` yêu cầu xác nhận rõ ràng trong UI + ghi audit; ghi nhận bên nhận vào hồ sơ xử lý |
| URL trỏ ra nước ngoài | Kiểm quốc gia của IP đích → cảnh báo về nghĩa vụ đánh giá tác động chuyển dữ liệu xuyên biên giới. **Cảnh báo, không chặn** — quyết định là của Bên Kiểm soát dữ liệu, không phải của công cụ |
| Chủ thể đã rút đồng ý | Sự kiện `submission.created` vẫn gửi (siêu dữ liệu), nhưng `answers` bị lược nếu mục đích tương ứng đã bị rút |
| Nhật ký giao hàng chứa DLCN | `response_snippet` cắt 1 KB; `payload` áp cùng retention của submission |
