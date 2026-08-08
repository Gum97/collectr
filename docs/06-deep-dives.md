# 6. Deep dive các điểm nóng

Năm điểm rủi ro nhất của **hệ thống này cụ thể**, không phải danh sách chung chung.

---

## 6.1 🔥 Redirect path

Mục tiêu p99 < 80ms ở burst 500 RPS.

### Trình tự

```
1. Parse Host + code             → key  l:{domain_id}:{code}
2. Redis GET (cache-aside)       → TTL 300s ± 20% jitter
   miss → Postgres unique index (~1–3ms) → SETEX
3. Negative cache                → code không tồn tại: cache "__nil__" TTL 30s
4. Kiểm trạng thái               → expired: 410 · disabled: 410 · legal_hold: 451
5. Sinh visit_id + visit_token   → HMAC, TTL 30′
6. XADD event vào Redis Stream   → timeout 5ms, fire-and-forget
7. Trả 302
```

**Negative cache là bắt buộc, không phải tối ưu hóa.** Không có nó, kẻ dò link brute-force bypass hoàn toàn cache và đập thẳng Postgres — đúng kiểu tấn công mà một hệ thống chứa dữ liệu cá nhân sẽ hứng.

**TTL jitter ±20%** để các key nạp cùng lúc (sau deploy/restart) không hết hạn cùng lúc.

**Invalidation:** `DEL` key khi update/delete link (xóa, không update — update đua với concurrent fill có thể để lại giá trị cũ vĩnh viễn). TTL 300s là lưới an toàn cho path nào quên.

### Chuỗi suy giảm khi analytics hỏng

```
Redis XADD lỗi/timeout  → buffer channel in-process (cap 10k)
                        → worker flush batch vào analytics.events
buffer đầy              → DROP event, tăng analytics_events_dropped_total
Postgres lookup lỗi     → nếu có bản trong in-process LRU → phục vụ stale
                        → nếu không → 503 (lỗi thật, redirect không thể đoán bừa)
```

> **Analytics là best-effort và được phép mất. Consent/submission là zero-loss và dùng outbox trong transaction.** Phát biểu rõ ràng sự bất đối xứng này là điều quan trọng nhất trong toàn bộ mục 6.1 — thiết kế bôi đều một mức đảm bảo lên cả hai sẽ trả giá bằng latency hoặc bằng mất dữ liệu pháp lý.

### 302 chứ không 301

| | 301 Permanent | 302 Found |
|---|---|---|
| Browser cache | Có → click lặp lại không tới server | Không |
| Tải server | Thấp nhất | Cao hơn |
| Analytics | Mất click lặp lại | Đầy đủ |
| **Thu hồi link** | **Không thể** — browser đã cache vĩnh viễn | Có hiệu lực ngay |

Chọn **302**: cần đếm click lặp lại cho funnel, cần expiry có hiệu lực ngay, và **cần thu hồi link ngay lập tức khi có yêu cầu xóa dữ liệu**. Lý do thứ ba là lý do tuân thủ — nó biến 302 từ mặc định hợp lý thành yêu cầu bắt buộc.

Link hết hạn → **410 Gone**. Dọn lười (kiểm khi click) + sweeper hằng đêm. Không bao giờ quét liên tục.

### Hot key

Một link viral chiếm 90% traffic → dồn vào một key Redis. Redis một node ~100k ops/s, ở 500 RPS burst là dư 200 lần. **Không làm gì bây giờ.** Ngưỡng thêm in-process LRU (otter/ristretto, 10k entry, TTL 10s): khi redirect vượt 5.000 RPS.

### Quyền riêng tư trong chính tracking

- Không lưu IP đầy đủ — chỉ prefix `/24` + country từ GeoIP offline.
- Không cookie bên thứ ba. `visit_token` là HMAC trong query string, TTL 30 phút.
- `visit_id` không nối được giữa các workspace (HMAC có pepper riêng theo tenant).

Thu hẹp phạm vi dữ liệu cá nhân mà nền tảng **tự tạo ra** rẻ hơn nhiều so với phải xin đồng ý cho chính tracking của mình.

### Abuse

- Rate limit tạo link: theo user + theo IP (endpoint rút gọn URL luôn hút spam).
- Từ chối redirect loop: link trỏ về chính domain link của hệ thống.
- Nếu chạy public: kiểm target URL qua danh sách chặn cục bộ (không gọi API bên ngoài — sẽ làm rò dữ liệu ra ngoài biên giới hạ tầng).

---

## 6.2 🔥 Schema versioning sống chung với conditional logic

Đây là chỗ đa số form builder sập, và là yêu cầu trọng tâm của sản phẩm.

### Ba bất biến (toàn bộ câu trả lời nằm ở đây)

1. `field_id` / `option_id` là **ULID ổn định, không tái sử dụng**. Đổi label không sinh id mới.
2. Câu trả lời lưu **id**, không lưu label: `{"fld_used": "opt_yes"}`.
3. **Version published bất biến.** Sửa form = tạo version mới. Submission pin `form_version_id` → tự diễn giải được mãi mãi.

> Hệ quả: **không bao giờ migrate dữ liệu cũ theo schema mới.** Dữ liệu cũ được thu thập dưới một văn bản đồng ý cụ thể; viết lại nó vừa phá bằng chứng pháp lý vừa là nguồn bug bất tận. Ta migrate *cách hiển thị*, không migrate *dữ liệu*.

### Ma trận tương thích khi sửa form

`POST /draft/publish` chạy diff và phân loại; UI cảnh báo trước khi publish.

| Thay đổi | Loại | Bản ghi cũ | Grid hiển thị |
|---|---|---|---|
| Đổi label / help / thứ tự | non-breaking | không ảnh hưởng | dùng label mới nhất, tooltip label cũ |
| Thêm field optional | non-breaking | không ảnh hưởng | cột mới, bản ghi cũ = `—` |
| Thêm option vào choice | non-breaking | không ảnh hưởng | — |
| Thêm rule mới | non-breaking | không ảnh hưởng | — |
| **Xóa field** | breaking | **dữ liệu giữ nguyên** | cột đánh dấu `(gỡ từ v4)`, mặc định ẩn, bật lại được |
| **Đổi type** (text → choice) | breaking | dữ liệu giữ nguyên | **tách hai cột `fld_x@v1-3` và `fld_x@v4+`** — không ép kiểu |
| **Xóa option đang được rule tham chiếu** | **chặn publish** | — | phải sửa rule trước |
| optional → required | breaking | bản ghi cũ **không bị coi là invalid** | — |
| Xóa page đang là target của `goto` | **chặn publish** | — | — |
| Thêm field `sensitive:true` | breaking | — | bắt buộc cập nhật văn bản đồng ý → version consent mới |

### Validate graph rẽ nhánh lúc publish

Bắt lỗi ở thời điểm publish, không để nó thành lỗi runtime của người dùng cuối:

```
1. Dangling reference   mọi rule.when.field, rule.*.target tồn tại trong version này
2. Cycle detection      đồ thị goto giữa các page phải là DAG (DFS)
3. Unreachable page     page không tới được từ page đầu → cảnh báo
4. Required-but-hidden  field required mà MỌI đường đi đều ẩn nó → CHẶN publish
                        (lỗi kinh điển khiến form không bao giờ submit được)
5. Consent block        có field pii → bắt buộc consent block
                        có field sensitive → bắt buộc thông báo dữ liệu nhạy cảm
6. Limits               ≤ 200 field, ≤ 300 rule → chi phí evaluate có chặn trên
```

Kiểm 4 là kiểm quan trọng nhất và cũng là kiểm mà hầu hết công cụ bỏ sót.

### Đánh giá rule ở server — không tin client

```go
// internal/modules/forms/engine — hàm THUẦN, deterministic, không I/O, fuzz-test được
func Evaluate(schema Schema, answers map[string]any) (visible []FieldID, path []PageID, err error)
```

Luồng submit:

```
1. Nạp form_versions.schema theo form_version_id client gửi
2. visible, path := Evaluate(schema, answers)
3. LOẠI BỎ mọi answer nằm ngoài `visible`   ← quan trọng nhất
4. Kiểm required CHỈ trên `visible`
5. Lưu visible_fields cùng submission
```

**Bước 3 là một kiểm soát bảo mật, không phải dọn dẹp.** Nếu tin client về việc field nào đã hiển thị, kẻ tấn công có thể submit thẳng field thuộc nhánh mà họ chưa bao giờ nhìn thấy văn bản đồng ý tương ứng — nghĩa là hệ thống lưu dữ liệu cá nhân không có căn cứ đồng ý.

Client chạy **cùng engine đó** (biên dịch Go → WASM, hoặc port có golden test chung) → UX phản hồi tức thời, server vẫn là trọng tài. Golden test dùng chung fixture cho cả hai phía là thứ giữ chúng không lệch nhau.

**Toán tử cho phép** (whitelist, không phải biểu thức tùy ý — tránh biến rule engine thành công cụ thực thi mã):
`eq · neq · in · not_in · gt · gte · lt · lte · between · contains · is_empty · is_not_empty`

**Hành động:** `show · hide · require · optional · goto · end`

**Điều hướng mặc định là thuộc tính của trang, không phải của rule.** Mỗi page có `next` (tùy chọn); rỗng = trang kế tiếp theo thứ tự khai báo. Phát hiện khi implement: nếu chỉ có fallthrough theo thứ tự khai báo, người trả lời "đã dùng sản phẩm" đi hết nhánh của mình rồi **rơi thẳng vào nhánh "vì sao chưa dùng"** — vì trang đó tình cờ được khai báo kế tiếp. Thứ tự khai báo là chuyện bố cục, làm mặc định điều hướng thì sai. Google Forms và MS Forms đều đặt nó ở cấp section vì lý do này.

Đánh giá một lượt theo thứ tự page trong `path`, thứ tự rule khai báo. Không đệ quy — cycle đã bị chặn lúc publish.

### Version đổi giữa lúc người dùng đang điền

```
Client gửi form_version_id = fv_3, live hiện tại là fv_4:

  fv_3 published & chưa retired  → CHẤP NHẬN, validate theo fv_3,
                                    đánh dấu meta.stale_version = true
  fv_3 đã retired (gỡ khẩn cấp)  → 409 form_version_retired + trả schema mới
  consent document đã đổi        → 409 consent_document_changed
```

Chấp nhận version cũ được **vì version bất biến** — không có gì mơ hồ để giải quyết. Chỉ văn bản đồng ý mới chặn cứng, vì "họ đồng ý cái gì" phải chính xác tuyệt đối, không có chỗ cho xấp xỉ.

### Grid nhiều version — column registry

```
columns(form) = ∪ fields của MỌI version
                giữ thứ tự xuất hiện lần đầu
                metadata (label, type) lấy từ version mới nhất có chứa field đó
                kèm present_in_versions = [1,2,3]
```

Ngữ nghĩa ô, ba trạng thái không nhập nhằng:

| Điều kiện | Ô | Nghĩa |
|---|---|---|
| `field_id ∉ schema(version bản ghi)` | `—` | không hỏi ở version này |
| `∈ schema` nhưng `∉ visible_fields` | `∅` | ẩn theo nhánh rẽ |
| `∈ visible_fields`, answer rỗng | `""` | có hỏi, bỏ trống |

Export CSV luôn kèm cột `_form_version` và một sheet mô tả schema từng version. Người phân tích dữ liệu 6 tháng sau sẽ cần đúng thứ đó.

---

## 6.3 🔥 Consent & Data Subject Rights

### Submit là một transaction duy nhất

```go
tx := db.Begin()
  core.ReserveIdempotencyKey(tx, tenant, "submission", key, hash)  // UNIQUE = lock; KHÔNG SELECT-then-INSERT
  subject := consent.UpsertSubject(tx, tenant, "phone", answers[identifierField])
  dek     := crypto.GetOrCreateDEK(tx, subject)                    // AES-256-GCM, bọc bởi TENANT_KEK
  forms.InsertSubmission(tx, …, answers, seal(dek, sensitiveFields), visibleFields, purgeAt)
  consent.Record(tx, …)                                            // 1 dòng / mục đích, kèm evidence
  audit.Write(tx, "submission.created", …)                         // hash chain
  core.EnqueueOutbox(tx, "submission.created", …)
tx.Commit()
```

**Token phạm vi hẹp cho receipt link.** Link trả về lúc submit chỉ mở được **đúng bản ghi đó**, không liệt kê được lịch sử. Một link nằm trong hộp thư bị chiếm quyền không nên trở thành chìa khóa đọc toàn bộ quan hệ của người đó với doanh nghiệp.

> **Không bao giờ tồn tại submission thiếu consent record, và ngược lại.** Đây chính là lý do chọn PostgreSQL ở [bước 4](04-data-model.md#41-chọn-db). Nếu tách consent thành microservice ngay từ đầu, bất biến này biến thành bài toán saga — và rủi ro không còn là dữ liệu lệch, mà là rủi ro pháp lý.

### Bằng chứng đồng ý phải "in ra được"

`consent.records.evidence` + `consent.documents.body_html` (bất biến) + `form_versions.schema` (bất biến) đủ để **tái dựng chính xác trang mà chủ thể đã nhìn thấy** tại thời điểm đồng ý, render ra PDF/HTML theo yêu cầu.

`rendered_hash` được client tính trên đúng DOM đã hiển thị và server đối chiếu lại với hash của `body_html` — nếu lệch, từ chối submit. Không có bước này thì "bằng chứng" chỉ là lời khai của server.

### Rút đồng ý

- Append dòng `action='withdrawn'`, **không bao giờ sửa/xóa dòng cũ** — việc đã từng đồng ý là một sự thật lịch sử cần giữ.
- Cập nhật `consent.current_consents` trong cùng transaction.
- Không ảnh hưởng tính hợp pháp của việc xử lý trước đó → dữ liệu đã thu không tự động bị xóa, nhưng **dừng xử lý cho mục đích đó**: mọi export/đồng bộ phải gọi `ConsentChecker.HasActive(subject, purpose)` trước.
- Nếu mục đích bị rút là mục đích **duy nhất** làm căn cứ giữ dữ liệu → sinh DSR request `erase` tự động.

### Xóa dữ liệu — và vấn đề backup

Đây là câu hỏi khó nhất của mọi hệ thống tuân thủ: *"xóa" nghĩa là gì khi dữ liệu còn nằm trong bản backup của 3 tuần trước?*

| Phương án | Cách làm | Ưu | Nhược |
|---|---|---|---|
| Soft delete | đánh cờ `erased` | đơn giản | **không phải xóa**. Không chấp nhận được |
| Hard delete + backup retention ngắn | `DELETE` + giữ backup 30 ngày | đơn giản, đủ cho dữ liệu thường | còn tồn tại trong backup tối đa 30 ngày |
| **Crypto-shredding** | mỗi chủ thể có DEK riêng; xóa DEK → ciphertext không còn khóa để giải | xóa **tức thì** trong CSDL chính, và trong mọi backup chụp **sau** thời điểm xóa | không khôi phục được (đúng ý đồ); thêm quản lý khóa; mất `TENANT_KEK` = mất toàn bộ |

> [!WARNING]
> **Backup chụp TRƯỚC thời điểm xóa vẫn khôi phục được.** Bản backup đó chứa
> cả ciphertext lẫn `dek_wrapped`, và `TENANT_KEK` không được xoay vòng — nên
> phục hồi backup là phục hồi luôn dữ liệu "đã xóa".
>
> Nói với chủ thể dữ liệu hoặc cơ quan quản lý rằng việc xóa là không thể đảo
> ngược **chỉ đúng khi** các bản backup trước đó đã hết hạn lưu, hoặc `TENANT_KEK`
> đã được xoay. Chưa có mã xoay khóa trong repo này.
>
> Trước đây tài liệu và comment trong mã đều khẳng định ngược lại. Đó là một
> tuyên bố sai về mặt pháp lý, không chỉ là một lỗi kỹ thuật.

**Chọn kết hợp:**
- Field `sensitive:true` + file upload → mã hóa bằng DEK riêng của chủ thể → **crypto-shred khi xóa**.
- Phần còn lại → hard delete + backup retention 30 ngày, ghi rõ trong chính sách.

```
Erasure flow:
  1. DELETE forms.submissions / files.files thuộc chủ thể (hard delete)
  2. DELETE consent.data_subjects.dek_wrapped     ← ciphertext trong backup thành rác
  3. GIỮ LẠI: consent.records (bằng chứng đã từng đồng ý — nghĩa vụ chứng minh),
              audit.entries (bất biến), nhưng đã pseudonymised: chỉ còn subject_id,
              identifier_hash bị xóa nên không truy ngược ra người thật
  4. GHI audit "subject.erased" + đóng dsr_request
```

Bước 3 là chỗ hai nghĩa vụ mâu thuẫn nhau — quyền được xóa và nghĩa vụ chứng minh đã có đồng ý. Giải bằng cách giữ bản ghi ở dạng **không còn liên kết được với người thật** (identifier hash bị xóa cùng DEK). Cần luật sư xác nhận cách xử lý này cho ngữ cảnh Việt Nam.

### Audit log bất biến

```
hash_n = sha256(hash_{n-1} ‖ canonical_json(entry_n))
```
- DB role của app chỉ có `INSERT` + `SELECT` trên `audit.entries`.
- Job hằng ngày: ký checkpoint `(tenant, max_seq, hash)` bằng khóa riêng, lưu ra ngoài DB.
- `POST /api/v1/audit/verify` duyệt lại chuỗi → phát hiện mọi sửa/xóa.

Tamper-**evident**, không phải tamper-proof. Ai có quyền superuser DB vẫn dựng lại được toàn bộ chuỗi. Muốn mạnh hơn thì phải ghi ra hệ thống append-only bên ngoài — ngoài scope MVP, nhưng checkpoint đã ký là bước chuẩn bị cho việc đó.

### Chuẩn hóa identifier — điều kiện để quyền truy cập có nghĩa

`0902000111`, `+84 902 000 111` và `84902000111` là **một người**. Nếu không chuẩn hóa trước khi HMAC, một yêu cầu truy cập sẽ trả về một nửa số bản ghi của họ — và đó là **không đáp ứng được quyền**, chứ không phải lỗi hiển thị. Chuẩn hóa: lowercase + trim cho email; bỏ ký tự không phải số, bỏ tiền tố `84`/`0` cho số điện thoại.

### Xác thực danh tính self-service — chống enumeration

Đây là điểm nóng bảo mật số một của module DSR: một endpoint "cho tôi biết dữ liệu của email này" là món quà cho kẻ tấn công.

```
POST /api/dsr/identify {workspace, identifier}
  → LUÔN 202, thời gian phản hồi không đổi (constant-time), không tiết lộ tồn tại hay không
  → nếu tồn tại: gửi magic link qua chính kênh đó (email/SMS)
  → token: single-use, TTL 15′, gắn với (tenant, subject)
  → rate limit: 3 lần / identifier / giờ  VÀ  10 lần / IP / giờ
  → mọi lần thử ghi audit
```
Session sau xác thực chỉ thấy submission của **đúng chủ thể đó, trong đúng workspace đó**. Kiểm quyền sở hữu ở tầng object, không tin `id` trong request — đây là lớp lỗ hổng API phổ biến nhất.

`receipt_token` trả về lúc submit là đường tắt tiện lợi (không cần OTP lại) nhưng quyền hạn hẹp hơn: chỉ xem/sửa **đúng submission đó**, không liệt kê được các submission khác.

### Retention

```
purge_at tính TẠI THỜI ĐIỂM SUBMIT từ forms.retention_days
  → đổi chính sách KHÔNG hồi tố cho bản ghi cũ (trừ khi admin chủ động chạy re-apply)
Sweeper hằng ngày: WHERE purge_at < now() AND status = 'active'
  → retention_action = 'delete'    → hard delete + crypto-shred
  → retention_action = 'anonymize' → xóa field pii/sensitive, giữ answer thống kê
  → ghi audit mỗi lô
```
`purge_at` là cột vật lý chứ không tính động vì (a) index được → sweeper rẻ, (b) đổi chính sách không âm thầm xóa mất dữ liệu cũ.

---

## 6.4 File upload — local-first, S3 sau

### Vì sao không MinIO trong MVP

Object storage + presigned URL tồn tại để **gỡ bandwidth khỏi app server**. Số liệu [2.3](02-estimation.md#23-bandwidth): 1 GB/ngày, đỉnh 40 Mbps trong vài chục giây. Một app Go stream multipart thẳng ra disk không hề hấn.

Thêm một lý do nữa, và nó quan trọng hơn bandwidth:

> **Presigned PUT thẳng lên object storage nghĩa là client gửi plaintext tới nơi app không chạm vào.** App mất khả năng (a) kiểm magic bytes và (b) mã hóa bằng DEK của chủ thể. Với file có thể chứa dữ liệu nhạy cảm (CV, giấy tờ, ảnh chụp), đánh đổi này không đáng ở scale hiện tại.

Nên **kể cả sau khi bật S3/MinIO, upload vẫn đi qua app** để mã hóa; chỉ *download* chuyển sang presigned GET. Đây là lệch có chủ đích khỏi khuyến nghị chuẩn, với lý do cụ thể chứ không phải bỏ sót.

### `Storage` interface — đổi driver bằng env

```go
// internal/platform/storage
type Storage interface {
    Put(ctx context.Context, key string, r io.Reader, size int64, ct string) error
    Get(ctx context.Context, key string) (io.ReadCloser, error)
    Delete(ctx context.Context, key string) error
    SignedURL(ctx context.Context, key string, ttl time.Duration) (string, error)
}
```

| Driver | `Put` | `SignedURL` |
|---|---|---|
| `local` | ghi `/data/files/<key>` (`O_TMPFILE` + rename, atomic) | `/api/pub/files/{id}?t=<HMAC>&exp=<ts>`; app xác thực rồi trả `X-Accel-Redirect` để Caddy serve — byte không đi qua Go |
| `s3` | PutObject tới S3/MinIO | presigned GET thật |

**Cùng một contract** → đổi `STORAGE_DRIVER=local` sang `s3` không sửa một dòng code gọi. Đây là toàn bộ giá trị của việc trừu tượng hóa sớm ở đúng một chỗ.

### Đã hiện thực (v0.8)

`Storage` interface + driver `local`. Driver `s3` **chưa có** — chọn `STORAGE_DRIVER=s3` thì app từ chối khởi động kèm thông báo rõ, thay vì chạy với một stub âm thầm làm mất tệp.

Mỗi tệp có **DEK riêng**, bọc bởi tenant KEK — không dùng DEK của chủ thể, vì lúc upload người trả lời chưa khai danh tính, chưa có chủ thể nào để gắn khóa vào. Xóa dữ liệu hủy DEK của tệp → ciphertext (kể cả trong backup) không đọc được nữa; sweeper thu hồi dung lượng sau.

### Ngưỡng bật S3/MinIO — quyết định bằng số, không bằng cảm giác

| Điều kiện | Vì sao |
|---|---|
| Dung lượng file > **300 GB** | Disk một node bắt đầu là bài toán vận hành |
| Chạy **> 1 app instance** | Local disk không chia sẻ được — đây là ngưỡng cứng nhất |
| Cần backup file tách khỏi backup máy | S3 versioning + lifecycle rẻ hơn tự làm |
| Upload đỉnh > **200 Mbps** | Bandwidth app bắt đầu là bottleneck |

Chạm bất kỳ điều nào → đổi env, chạy job migrate `local → s3` (đọc `files.storage_key`, copy, verify checksum), xong.

### Kiểm soát bắt buộc ngay từ MVP

1. **Magic bytes**, không tin `Content-Type` hay đuôi file. Whitelist theo cấu hình của field.
2. **Giới hạn kích thước hai tầng**: Caddy `request_body max_size` và app (theo `max_mb` của field).
3. **Ảnh được re-encode** để vô hiệu payload nhúng.
4. **Không bao giờ serve file từ domain chính** — dùng subdomain riêng để cô lập XSS; luôn `Content-Disposition: attachment` + `X-Content-Type-Options: nosniff`.
5. **Orphan sweeper**: `status='pending'` quá 24h → xóa cả row lẫn byte. Client xin upload rồi bỏ giữa chừng là chuyện bình thường, phải có người dọn.
6. **Dedupe theo sha256** — cùng checksum trong cùng tenant → trỏ chung `storage_key`, đếm tham chiếu khi xóa. (Không dedupe chéo tenant: rò rỉ thông tin về việc tenant khác có cùng file.)

---

## 6.5 Analytics funnel async

### Đường đi

```
click / form_view / form_start / submit
  → Redis Stream `ev`  (XADD MAXLEN ~1e6, fire-and-forget, timeout 5ms)
  → worker consumer group → batch INSERT analytics.events (ON CONFLICT DO NOTHING theo event_id)
  → rollup worker mỗi 30s: RECOMPUTE các bucket 5 phút đã đóng từ analytics.events
  → funnel query đọc analytics.funnel_rollups → vài ms
```

### Vì sao "recompute bucket đã đóng" chứ không "cộng dồn"

Cộng dồn (`count = count + n`) **không idempotent**: worker crash sau khi ghi nhưng trước khi commit offset → đếm hai lần. Recompute cả bucket bằng `DELETE + INSERT` (hoặc `INSERT … ON CONFLICT DO UPDATE SET count = excluded.count`) là **idempotent tự nhiên** — chạy lại bao nhiêu lần cũng ra cùng kết quả.

Chi phí: quét lại các event trong một bucket 5 phút ≈ 1.200 row → không đáng kể. Đổi lấy việc không bao giờ phải debug "vì sao số liệu lệch".

Chỉ recompute bucket **đã đóng** (`bucket_end < now() - 1 phút`) để tránh đọc dữ liệu đang tới.

### Nối funnel

```
click     → sinh visit_id, đặt vào visit_token (HMAC, TTL 30′), gắn vào URL đích
form_view → client gửi kèm visit_token → server verify HMAC → lấy visit_id
submit    → submission.visit_id
funnel    → GROUP BY theo visit_id trong cửa sổ 30 phút
```
Không cookie, không định danh vĩnh viễn. Đánh đổi: người dùng mở lại link sau 30 phút bị đếm là visit mới → tỉ lệ chuyển đổi hơi thấp hơn thực tế. Chấp nhận được, và ghi rõ trong tooltip của dashboard — số liệu analytics mà không nói rõ định nghĩa là số liệu sai.

### Idempotency của event

`event_id` (ULID sinh ở client) + `UNIQUE (tenant_id, event_id)` → retry của beacon không double-count. Insert trùng bị `ON CONFLICT DO NOTHING` nuốt, không phải lỗi.

### Retention

`analytics.events` partition theo ngày → xóa retention = `DROP PARTITION` (tức thì) thay vì `DELETE` hàng triệu dòng (khóa bảng, phình WAL). `funnel_rollups` nhỏ, giữ vĩnh viễn.

---

## 6.6 Rà soát security checklist

| Mục | Trạng thái trong thiết kế |
|---|---|
| **AuthN** | Session cookie (`HttpOnly`, `Secure`, `SameSite=Lax`) cho admin · PAT cho API · magic-link token cho chủ thể dữ liệu |
| **AuthZ đối tượng** | Kiểm quyền sở hữu **trên từng object**, không tin `id` trong request. RLS là lưới an toàn thứ hai. Đây là lớp lỗ hổng API số một |
| **Input validation** | Kích thước + kiểu + format ở biên; parameterized query 100% (pgx, không nối chuỗi); upload kiểm magic bytes |
| **Secrets** | `TENANT_KEK`, DB password, HMAC pepper qua env/secret file. Không có gì trong git. `TENANT_KEK` mất = mất toàn bộ dữ liệu nhạy cảm → phải backup riêng, tách khỏi backup DB |
| **Transport & storage** | TLS bắt buộc (Caddy tự động). Mật khẩu: argon2id. Field `sensitive:true` + file: AES-256-GCM bằng DEK riêng chủ thể. Không log token/PII |
| **Rate limiting** | Mọi endpoint public ghi đều có giới hạn. `/api/dsr/identify` chặt nhất. Redis token bucket; Redis chết → **fail-closed cho DSR**, fail-open cho redirect |
| **Least privilege** | DB role riêng theo schema; `audit` chỉ INSERT+SELECT; app user không có `DROP` |
| **Auditability** | Hash chain bất biến, gồm cả sự kiện **đọc hàng loạt** (export) |
| **Dependency hygiene** | Pin version, `govulncheck` trong CI, Dependabot, base image distroless |
| **Chống enumeration** | short code random 7 ký tự · negative cache · `/api/dsr/identify` luôn 202 constant-time · form không tồn tại và form đã đóng trả cùng một 404 |

**Fail-closed cho DSR, fail-open cho redirect** là một quyết định có chủ đích: rate limiter chết thì cho phép link tiếp tục hoạt động (availability), nhưng chặn đường dò tìm dữ liệu cá nhân (confidentiality). Hai hướng ngược nhau vì cái giá của sai lầm ở hai nơi khác nhau.
