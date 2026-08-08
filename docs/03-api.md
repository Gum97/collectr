# 3. Thiết kế API

REST + JSON. Lỗi theo RFC 7807 (`application/problem+json`), mọi response có `trace_id`.

## 3.1 Ba nhóm auth — ranh giới bảo mật quan trọng nhất

| Nhóm | Prefix | Auth | Rate limit |
|---|---|---|---|
| Public runtime | `/r/*`, `/f/*`, `/api/pub/*` | không auth | theo IP + theo link/form |
| Data-subject self-service | `/api/dsr/*` | magic-link token (OTP email/SMS), single-use, TTL 15′ | rất chặt — chống enumeration |
| Management | `/api/v1/*` | session cookie / PAT, scope theo `workspace_id` | theo user |

Public API **không bao giờ tiết lộ sự tồn tại** của email/phone/form ẩn. Mọi phản hồi cho input không tồn tại phải không phân biệt được với input tồn tại.

---

## 3.2 🔥 `GET /r/{code}` — redirect (hot path)

```http
GET /r/Xk9mQ2v HTTP/1.1
Host: links.acme.vn

HTTP/1.1 302 Found
Location: https://acme.vn/landing?utm_source=qr&cx=<visit_token>
Cache-Control: private, no-store
```

| Code | Khi nào |
|---|---|
| `302` | OK. **Không dùng 301** — xem [6.1](06-deep-dives.md#61--redirect-path) |
| `404` | code không tồn tại |
| `410 Gone` | hết hạn hoặc bị vô hiệu hóa |
| `451` | bị gỡ theo yêu cầu pháp lý / DSR |

- Uniqueness của `code` scope theo **`(domain_id, lower(code))`**, không toàn cục → hai tenant dùng chung alias trên hai domain khác nhau được.
- `visit_token` = `base64url(HMAC-SHA256(visit_id ‖ link_id ‖ exp))`, TTL 30′. Dùng để nối funnel **mà không cần cookie bên thứ ba**.
- Nếu link gắn form thì `Location` là URL form nội bộ, `visit_token` đi kèm.

## 3.3 `GET /q/{code}` — QR

```http
GET /q/Xk9mQ2v?format=png&size=512&ec=M
→ 200 image/png   (Cache-Control: public, max-age=86400)
→ 200 image/svg+xml khi format=svg
```
QR sinh on-the-fly (thư viện Go thuần, ~1ms), cache ở tầng HTTP. Không lưu file.

---

## 3.4 `GET /api/pub/forms/{public_id}` — schema để render

```http
200 OK
ETag: "fv_01JB7X…"
Cache-Control: no-store        # schema có thể chứa văn bản đồng ý → không cache dùng chung
```
```json
{
  "form":    {"public_id": "fm_01J…", "title": "Đăng ký dùng thử"},
  "version": {"id": "fv_01JB7X…", "no": 3},
  "schema":  { "pages": [...], "fields": {...}, "rules": [...], "limits": {...} },
  "consent": {
    "document_id": "cd_01J…",
    "body_html": "…",
    "purposes": [
      {"code": "service",   "name": "Xử lý yêu cầu dùng thử", "required": true},
      {"code": "marketing", "name": "Gửi thông tin khuyến mại", "required": false}
    ],
    "sensitive_notice": "Biểu mẫu này thu thập dữ liệu cá nhân nhạy cảm: tình trạng sức khoẻ."
  }
}
```
`404` nếu form không tồn tại **hoặc** đã đóng — không phân biệt hai trường hợp.

## 3.5 🔥 `POST /api/pub/forms/{public_id}/submissions` (hot path)

```http
POST /api/pub/forms/fm_01J…/submissions
Idempotency-Key: 8f14e45f-…        # BẮT BUỘC
Content-Type: application/json
```
```json
{
  "form_version_id": "fv_01JB7X…",
  "visit_token": "…",
  "answers": {
    "fld_name":  "Nguyễn Văn A",
    "fld_phone": "0901234567",
    "fld_used":  "opt_yes",
    "fld_tags":  ["opt_a", "opt_c"],
    "fld_cv":    {"file_id": "fl_01J…"}
  },
  "consents": [
    {"purpose": "service",   "granted": true},
    {"purpose": "marketing", "granted": false}
  ],
  "consent_proof": {"document_id": "cd_01J…", "rendered_hash": "sha256:9a1f…"}
}
```

```json
201 Created
{
  "submission_id": "sb_01J…",
  "receipt_token": "rt_…",                          // tra cứu lại bản ghi không cần OTP
  "manage_url": "https://forms.acme.vn/dsr?rt=rt_…"
}
```

| Code | Body | Ý nghĩa |
|---|---|---|
| `422` | `{"error":"validation_failed","fields":{"fld_phone":"required"}}` | Chỉ kiểm required trên **tập field thực sự hiển thị** do server tự tính |
| `409` | `{"error":"form_version_retired","current_version_id":"fv_01JC…","schema":{…}}` | Version bị gỡ khẩn cấp giữa lúc điền |
| `409` | `{"error":"consent_document_changed","document":{…}}` | Văn bản đồng ý đã đổi → **không được im lặng ghi nhận theo văn bản cũ** |
| `413` | | payload > 256 KB |
| `429` | `Retry-After` | rate limit |

Retry với cùng `Idempotency-Key`:
- cùng payload → phát lại nguyên văn response `201` gốc
- **khác payload** → `422 idempotency_key_reused` (chặn replay đổi nội dung)

## 3.6 `POST /api/pub/forms/{public_id}/uploads` — upload file

App nhận byte trực tiếp (local-first, xem [6.4](06-deep-dives.md#64-file-upload--local-first-s3-sau)).

```http
POST /api/pub/forms/{public_id}/uploads
Content-Type: multipart/form-data
X-Form-Version-Id: fv_01JB7X…
X-Field-Id: fld_cv
```
```json
201 {"file_id":"fl_01J…","size":1048576,"checksum":"sha256:…","expires_at":"…"}
415 loại file không được chấp nhận (kiểm bằng magic bytes, không tin extension)
413 vượt giới hạn của field
```
File ở trạng thái `pending` cho tới khi được tham chiếu bởi một submission. Orphan sweeper dọn sau 24h.

Tải về: `GET /api/pub/files/{file_id}?t=<HMAC>&exp=<ts>` — URL ký, TTL 10 phút. **Cùng contract với presigned URL của S3**, nên đổi driver không cần sửa code gọi.

## 3.7 `POST /api/pub/events` — beacon analytics

```json
{"events":[
  {"event_id":"ev_01J…","type":"form_view","form_version_id":"fv_…","visit_token":"…","ts":"2026-08-06T10:00:00Z"}
]}
→ 202 Accepted    // LUÔN 202 — analytics không bao giờ được làm hỏng UX
```
`event_id` do client sinh (ULID) → khóa idempotency chống double-count khi retry.

---

## 3.8 Data subject rights — `/api/dsr/*`

| Method | Path | Ghi chú |
|---|---|---|
| `POST` | `/api/dsr/identify` | `{workspace, identifier}` → **luôn `202`**, kể cả identifier không tồn tại |
| `POST` | `/api/dsr/session` | đổi magic token lấy session TTL 30′, token single-use |
| `GET` | `/api/dsr/me/submissions` | chỉ bản ghi của chính chủ thể, trong đúng workspace đó |
| `PATCH` | `/api/dsr/me/submissions/{id}` | quyền chỉnh sửa; ghi `submission_revisions` + audit |
| `GET` | `/api/dsr/me/consents` | lịch sử đồng ý/rút, bản in được (PDF/HTML) |
| `POST` | `/api/dsr/me/consents/{purpose}/withdraw` | rút đồng ý → `202 {request_id, effective_at}` |
| `POST` | `/api/dsr/me/requests` | `{type: access\|rectify\|erase\|restrict\|export\|object}` → `202 {request_id, due_at}` |
| `GET` | `/api/dsr/me/requests/{id}` | trạng thái + `due_at` |

`due_at = received_at + DSR_SLA_HOURS` (mặc định 72h, cấu hình được).

---

## 3.9 Management — `/api/v1/*`

```
POST   /api/v1/links                       {target_url, alias?, expires_at?, form_id?}
GET    /api/v1/links?cursor=&q=
PATCH  /api/v1/links/{id}
DELETE /api/v1/links/{id}                  → 204, soft delete, redirect trả 410

GET    /api/v1/links/{id}/stats            → lượt bấm theo thời gian, nguồn, referrer, trình duyệt
GET    /api/v1/links/stats?project_id=     → xếp hạng link theo lượt bấm
POST   /api/v1/projects/{id}/link-exports   → 202 {export_id}  báo cáo link ra Excel

GET    /api/v1/domains                     → tên miền phát mã, kèm số link đang dùng
POST   /api/v1/domains                     {host, is_default?}  → 201 | 409 host_taken
PUT    /api/v1/domains/{id}/default        → link TẠO MỚI dùng host này; link cũ giữ nguyên
DELETE /api/v1/domains/{id}                → 204 | 409 domain_in_use nếu còn link

POST   /api/v1/forms                       → tạo form + draft version 1
PUT    /api/v1/forms/{id}/draft            → lưu schema draft (không đụng bản live)
POST   /api/v1/forms/{id}/draft/validate   → 200 {ok:true} | 422 {issues:[…]}  (kiểm graph rẽ nhánh)
POST   /api/v1/forms/{id}/draft/publish    → 201 {version_id, no}  (published = BẤT BIẾN)
GET    /api/v1/forms/{id}/versions
GET    /api/v1/forms/{id}/versions/{a}/diff/{b}  → phân loại breaking / non-breaking

GET    /api/v1/forms/{id}/submissions?cursor=&filter=   → grid, column registry hợp nhất mọi version
POST   /api/v1/forms/{id}/exports          → 202 {job_id}   (async; GHI AUDIT: truy cập hàng loạt DLCN)
GET    /api/v1/exports/{job_id}            → 200 {status, download_url}

GET    /api/v1/forms/{id}/analytics/funnel?from=&to=&group_by=day

GET|POST /api/v1/consent/documents         // văn bản đồng ý, có version, bất biến sau publish
GET|POST /api/v1/consent/purposes          // mục đích + căn cứ pháp lý + retention

GET    /api/v1/dsr/requests?status=open&overdue=true
POST   /api/v1/dsr/requests/{id}/fulfill   {action, note}

GET    /api/v1/audit?cursor=&actor=&action=
POST   /api/v1/audit/verify                → kiểm tra tính toàn vẹn hash chain

GET    /api/v1/api-keys                    → danh sách key + quyền bị cấm cứng
POST   /api/v1/api-keys                    → 201 {key}  HIỆN MỘT LẦN DUY NHẤT
DELETE /api/v1/api-keys/{id}               → 204, thu hồi (giữ bản ghi cho audit)

GET    /api/v1/forms/{id}/files            → tệp đã nhận (không kèm URL tải)
```

### UTM đi xuyên qua link rút gọn

Tham số truy vấn trên link rút gọn được **chuyển tiếp** sang đích. Trước đây chúng bị bỏ hết: đội marketing gắn `utm_source` lên link rút gọn, và Google Analytics phía đích báo toàn bộ lưu lượng là direct — rồi shortener bị đổ lỗi cho một con số không ai giải thích được.

```
/r/abc?utm_source=facebook&utm_campaign=tet2026
    → https://acme.vn/landing?cx=…&utm_campaign=tet2026&utm_source=facebook
```

Tham số **đã có sẵn ở URL đích không bị ghi đè**: người tạo link đặt chúng có chủ đích, khách truy cập thêm cùng khóa không có quyền đè lên. `cx` (token phiên) và `src` (dấu QR) là của riêng shortener, không chuyển tiếp.

`utm_source`, `utm_medium`, `utm_campaign` được lưu vào sự kiện, mỗi giá trị cắt ở 120 ký tự — chúng đến thẳng từ query string, sao chép không giới hạn là mời người ta ghi một megabyte mỗi lượt bấm.

### Báo cáo link — hai nguồn, nói rõ ra

Phản hồi trả về **hai** con số lượt bấm, có chủ đích:

| Trường | Nguồn | Phạm vi |
|---|---|---|
| `clicks` | `funnel_rollups` | toàn bộ lịch sử |
| `breakdown_clicks` | `analytics.events` | chỉ trong hạn lưu sự kiện thô |

Mọi tỉ lệ (`qr_share`, `clicks_per_network`) chia cho `breakdown_clicks`, **không** cho `clicks`. Lần đầu tôi chia nhầm và endpoint trả về `repeat_rate: -3.127` — một tỉ lệ âm. Hai mẫu số phủ hai khoảng thời gian khác nhau thì không được đem chia cho nhau.

`breakdown_note` nói thẳng ngày bắt đầu của phần phân tích. Nếu không, biểu đồ phủ một năm nằm trên bảng referrer phủ 90 ngày sẽ đọc thành "trước tháng 3 không ai dẫn link về".

**`networks`, không phải `visitors`.** Hệ thống không nhận ra người quay lại và cố tình như vậy: `visit_id` sinh mới ở mỗi lượt chuyển hướng, nên đếm `visit_id` khác nhau sẽ **luôn** ra đúng bằng số lượt bấm và mọi "tỉ lệ quay lại" dựng trên đó đều bằng 0 về mặt cấu trúc. Nhận diện người qua nhiều lần truy cập cần cookie hoặc fingerprint — tức là theo dõi, cần căn cứ pháp lý, và là thứ sản phẩm này sinh ra để tránh. Đếm dải mạng vẫn trả lời được câu đáng hỏi: 900 lượt từ 4 dải là script hoặc một văn phòng, 900 lượt từ 300 dải mới là độ phủ.

Độ trễ: tối đa `BucketWidth` + `closedBucketLag` = **6 phút**. `clicks` và `breakdown_clicks` lệch nhau trong khoảng đó là bình thường.

### Tên miền

Thêm tên miền cần `member.manage`, không phải `link.write`: nó đổi thứ mà deployment trả lời, và cần thêm bản ghi DNS lẫn chứng chỉ. Người biên tập tạo được link thì không nên trỏ được tên miền mới vào chúng.

`host` là **duy nhất trên toàn deployment**, không phải theo tenant — redirect chỉ biết Host header, không có cách nào khác để quyết định mã đó thuộc về ai. Khi trùng, lỗi trả về không nói tên miền thuộc tenant nào, vì trên deployment dùng chung điều đó sẽ tiết lộ tên miền của tổ chức khác.

## 3.10 Tổng kết hot path

| Endpoint | Vì sao nóng | Ngân sách latency |
|---|---|---|
| 🔥 `GET /r/{code}` | 500 RPS burst, mọi link đều đi qua | p99 < 80ms |
| 🔥 `POST /api/pub/forms/{id}/submissions` | zero-loss + transaction đa bảng | p99 < 500ms |
| `GET /api/pub/forms/{id}` | 200 RPS burst | p99 < 300ms |

Mọi endpoint khác < 1 RPS — không tối ưu.
