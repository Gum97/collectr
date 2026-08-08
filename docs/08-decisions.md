# 8. Bảng quyết định

Mọi quyết định lớn, mỗi cái ≥ 2 phương án và lý do chọn.

## Kiến trúc

| Quyết định | Phương án | Chọn | Lý do |
|---|---|---|---|
| Kiểu hệ thống | Modular monolith · Microservices | **Modular monolith** | Không có team thứ hai, không có profile tải phân kỳ đã đo. Biên giới cưỡng chế bằng CI để tách được sau |
| Worker | Chung process app · Process riêng | **Process riêng** (cùng binary) | GC pause + long query của export ăn vào p99 redirect. Cờ `RUN_WORKER_INLINE` cho ai muốn 3 container |
| Queue | Postgres `SKIP LOCKED` · Redis Stream · Kafka | **Postgres cho job, Redis Stream cho event** | 4 job/s steady. Postgres queue chịu 1-2k msg/s. Kafka thừa 2 bậc độ lớn. Redis Stream cho event vì analytics được phép mất |
| Cross-module | SQL JOIN xuyên schema · Interface trong `contracts/` | **Interface** | Giữ đường tách service; ngăn coupling ngầm qua DB |

## Dữ liệu

| Quyết định | Phương án | Chọn | Lý do |
|---|---|---|---|
| DB chính | PostgreSQL+JSONB · MongoDB · Postgres+ClickHouse | **PostgreSQL + JSONB** | Cần transaction để `submission + consent` atomic — **ràng buộc pháp lý**, không phải sở thích |
| Multi-tenant | Shared DB + RLS · Schema/tenant · DB/tenant | **Shared DB + RLS** | 200 tenant / 80 GB. RLS là lưới an toàn chống rò rỉ chéo tenant — với DLCN đó là sự cố phải báo cáo, không phải bug thường |
| Short code | Counter→base62 · **Random 7 base62** | **Random** | Counter liệt kê được → lộ khối lượng kinh doanh + mỗi link là cửa vào form thu DLCN. p(va chạm) 1,4×10⁻⁷ ở 500k link |
| Dedupe URL | Có · Không | **Không** | Cùng URL cần nhiều link để có expiry/analytics/alias riêng |
| Analytics store | Bảng Postgres partitioned · ClickHouse | **Postgres** | 340k event/ngày. Rollup 5 phút trả funnel query trong ms |
| Retention raw event | DELETE theo lịch · **DROP PARTITION** | **DROP PARTITION** | DELETE hàng triệu dòng khóa bảng + phình WAL |

## Form versioning

| Quyết định | Phương án | Chọn | Lý do |
|---|---|---|---|
| Version published | Sửa được · **Bất biến** | **Bất biến** | Nền tảng của cả versioning lẫn bằng chứng pháp lý: tái dựng chính xác trang chủ thể đã thấy |
| Lưu câu trả lời | Theo label · **Theo `field_id`/`option_id`** | **Theo id** | Đổi label không làm hỏng bản ghi cũ |
| Dữ liệu cũ khi schema đổi | Migrate theo schema mới · **Giữ nguyên, migrate cách hiển thị** | **Giữ nguyên** | Dữ liệu cũ thu dưới một văn bản đồng ý cụ thể; viết lại là phá bằng chứng |
| Đánh giá conditional logic | Tin client · **Server đánh giá lại** | **Server** | Không thì có thể submit field ở nhánh chưa từng thấy văn bản đồng ý → lưu DLCN không có căn cứ |
| Engine client | Viết riêng · **Cùng source Go → WASM** | **Cùng source** + golden test chung | Giữ hai phía không lệch nhau |
| Version cũ khi đang điền | Từ chối · **Chấp nhận** (trừ khi retired) | **Chấp nhận** | Version bất biến nên không có gì mơ hồ. Riêng văn bản đồng ý đổi thì chặn cứng |
| Rule engine | Biểu thức tùy ý · **Whitelist toán tử** | **Whitelist** | Tránh biến rule engine thành công cụ thực thi mã |

## Consent & DSR

| Quyết định | Phương án | Chọn | Lý do |
|---|---|---|---|
| Vị trí module | Trộn vào forms · **Bounded context riêng** | **Riêng** | Ngôn ngữ khác, vòng đời khác, lý do thay đổi khác, yêu cầu bảo mật khác. Phụ thuộc một chiều |
| Ghi consent | Sau submit (async) · **Cùng transaction** | **Cùng transaction** | "Không tồn tại submission thiếu consent" là bất biến pháp lý |
| Rút đồng ý | UPDATE record · **APPEND record mới** | **Append** | Việc đã từng đồng ý là sự thật lịch sử phải giữ |
| Xóa dữ liệu | Soft delete · Hard delete + backup ngắn · **Crypto-shred cho nhạy cảm + hard delete phần còn lại** | **Kết hợp** | Soft delete không phải xóa. Crypto-shred xử lý được cả residue trong backup |
| Audit log | Bảng thường · **Hash chain + role chỉ INSERT** | **Hash chain** | Tamper-evident. Checkpoint ký hằng ngày lưu ngoài DB |
| Xác thực chủ thể | Trả lời thẳng · **Luôn 202 + magic link** | **Luôn 202** | Endpoint "cho tôi biết dữ liệu của email này" là quà cho kẻ tấn công |
| `purge_at` | Tính động khi query · **Cột vật lý** | **Cột vật lý** | Index được (sweeper rẻ) + đổi chính sách không âm thầm xóa dữ liệu cũ |

## Redirect & cache

| Quyết định | Phương án | Chọn | Lý do |
|---|---|---|---|
| HTTP status | 301 · **302** | **302** | Cần đếm click lặp, cần expiry hiệu lực ngay, và **cần thu hồi link ngay khi có yêu cầu xóa** |
| Cache pattern | Cache-aside · Write-through | **Cache-aside** + delete-on-write + TTL 300s ± jitter | Mặc định cho read path; TTL là lưới an toàn cho path quên invalidate |
| Negative cache | Không · **Có (TTL 30s)** | **Có** | Không có thì brute-force dò code bypass cache, đập thẳng Postgres |
| Ghi click | Sync vào DB · **Async qua Redis Stream** | **Async** | Không bao giờ để analytics chặn redirect. Có chuỗi suy giảm rõ ràng, cuối cùng là drop |
| Rate limiter khi Redis chết | Fail-open toàn bộ · **Fail-open redirect, fail-closed DSR** | **Tách hướng** | Giá của sai lầm khác nhau: link chết vs rò rỉ DLCN |

## File storage

| Quyết định | Phương án | Chọn | Lý do |
|---|---|---|---|
| Backend MVP | MinIO ngay · **Local disk + interface** | **Local disk** | 1 GB/ngày, đỉnh 40 Mbps. Object storage tồn tại để gỡ bandwidth khỏi app — chưa có bandwidth để gỡ |
| Upload path | Presigned PUT thẳng object storage · **Qua app** | **Qua app** (kể cả sau khi bật S3) | Presigned = client gửi plaintext → mất kiểm magic bytes + mất mã hóa bằng DEK. Không đáng với file có thể chứa DLCN nhạy cảm |
| Download | Qua app · **URL ký + `X-Accel-Redirect`** | **URL ký** | Byte không đi qua Go; cùng contract với presigned GET của S3 |
| Dedupe | Toàn cục theo sha256 · **Trong cùng tenant** | **Trong tenant** | Dedupe chéo tenant lộ thông tin tenant khác có cùng file |

## Vận hành

| Quyết định | Phương án | Chọn | Lý do |
|---|---|---|---|
| Rollup analytics | Cộng dồn · **Recompute bucket đã đóng** | **Recompute** | Cộng dồn không idempotent; recompute chạy lại bao nhiêu lần cũng đúng |
| Tracing | OpenTelemetry ngay · **`trace_id` + log có cấu trúc** | **`trace_id`** | Trong monolith, log tốt lo 90%. Áp monolith-first cho cả observability |
| Observability stack | Bắt buộc · **Profile tùy chọn** | **Tùy chọn** | Vận hành bộ quan sát tốn hơn vận hành app là một thất bại thiết kế |
| SLA tuân thủ | Hardcode 72h · **Env config** | **Env** | Số cụ thể cần luật sư xác nhận và có thể đổi theo hướng dẫn thi hành |
