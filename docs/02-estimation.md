# 2. Ước lượng back-of-envelope

Cơ sở: 200 workspace, mỗi ws ~10 form đang chạy + ~50 link ([giả định](01-requirements.md#13-giả-định-định-lượng)).

## 2.1 Traffic

| Luồng | /ngày | Trung bình | Peak ngày (×5) | Burst sự kiện |
|---|---|---|---|---|
| Click redirect `/r/{code}` | 200.000 | 2,3 RPS | ~12 RPS | **300–500 RPS** |
| Form view (60% click) | 120.000 | 1,4 RPS | ~7 RPS | 200 RPS |
| Submit (10% click) | 20.000 | 0,23 RPS | ~1,2 RPS | **50–100 RPS** |
| Event ingest (3 stage) | ~340.000 | 4,0 RPS | 20 RPS | 800 RPS |
| Admin / dashboard | ~4.000 | < 0,1 RPS | — | — |

**Read : write ≈ 10 : 1**, nhưng khác URL shortener thuần ở một điểm quan trọng: **mỗi read (click) đẻ ra một write (event)**. Tổng write ≈ 360k row/ngày ≈ 4,2 write/s.

Đối chiếu bảng throughput: Postgres một node chịu hàng trăm–hàng nghìn TPS ghi và 1k–10k+ QPS đọc có index.

> **Không có bottleneck về throughput. Bottleneck là burst và là latency của redirect.**

## 2.2 Storage (1 năm, đã +30% cho index/overhead)

| Bảng | Row/ngày | Bytes/row | Sau 1 năm |
|---|---|---|---|
| `links` | ~500 | 400 B | 0,07 GB (tổng 500k link ≈ 200 MB) |
| `submissions` (JSONB ~5 KB) | 20.000 | 5 KB | **47 GB** |
| `events` raw (giữ 90 ngày, partition drop) | 340.000 | 250 B | 11 GB steady-state |
| `funnel_rollups` | ~20.000 | 100 B | 0,9 GB |
| `consent_records` | ~25.000 | 800 B | 9,5 GB |
| `audit_entries` | ~50.000 | 500 B | 12 GB |
| **PostgreSQL tổng** | | | **~80 GB/năm** |

**File upload:** giả định 5% submission có file, trung bình 1 MB → 1.000 file/ngày → **1 GB/ngày ≈ 365 GB/năm**.

## 2.3 Bandwidth

| Luồng | Tính | Kết quả |
|---|---|---|
| Redirect | 12 RPS × 300 B | không đáng kể |
| Upload steady | 1.000 file/ngày × 1 MB | 1 GB/ngày ≈ **95 kbps trung bình** |
| Upload burst | 100 submit/s × 5% × 1 MB | **5 MB/s ≈ 40 Mbps** trong vài chục giây |
| Serve file (20% được xem lại) | 200 lượt/ngày × 1 MB | 200 MB/ngày, ~19 kbps |

> Đây là con số bác bỏ MinIO trong MVP. Object storage + presigned URL tồn tại để **gỡ bandwidth khỏi app server**. Ở 40 Mbps đỉnh, một app Go stream multipart thẳng ra disk (không buffer vào RAM) hoàn toàn không hề hấn. Chi tiết trade-off ở [6.4](06-deep-dives.md#64-file-upload--local-first-s3-sau).

## 2.4 Kết luận scale

> **Small (MVP)** theo bảng chuẩn: < 100 RPS, < 100 GB DB.
>
> Kiến trúc mặc định: **1 app + 1 PostgreSQL + 1 Redis trên một máy 4 vCPU / 8 GB RAM / 500 GB SSD.**

Dung lượng đĩa 500 GB nuôi được ~1 năm (80 GB DB + 365 GB file). Đây là ngưỡng đầu tiên phải theo dõi, không phải CPU.

## 2.5 Component nào được phép tồn tại

Nguyên tắc: mỗi component thêm vào phải trả lời được **"ở con số nào thì hệ thống hỏng nếu không có nó?"**

| Component | Con số biện minh | Không có nó thì hỏng ở đâu |
|---|---|---|
| **Redis** | burst 800 events/s; rate limit; single-flight | Postgres vẫn chịu được 500 QPS đọc có index → **cache lookup chưa bắt buộc**. Redis bắt buộc vì làm *buffer event* và *rate limiter* — hai thứ không nên đụng Postgres trên hot path |
| **Queue = bảng Postgres `SKIP LOCKED`** | 4 job/s steady, burst 48k message trong 60s | Postgres queue xử lý ~1–2k msg/s. Kafka thừa 2 bậc độ lớn cho tải này |
| **Worker process riêng** | rollup, retention, DSR SLA, orphan sweeper, export | Chạy chung web process → GC pause và long query của export sẽ ăn vào p99 redirect |
| ❌ **MinIO / S3** | 1 GB/ngày, 40 Mbps đỉnh | Chưa hỏng ở đâu cả. Ngưỡng bật: xem [7.4](07-operations.md#74-scaling-path) |
| ❌ **Read replica** | grid query 10k dòng ~50ms trên index | Chưa cần |
| ❌ **ClickHouse** | 340k event/ngày | Rollup 5 phút trên Postgres trả funnel query trong ms |
| ❌ **Microservices** | — | Không có team thứ hai, không có profile tải phân kỳ đã đo |

## 2.6 Kết quả đo (2026-08-08)

Kịch bản trong `load/`, chạy bằng `docker compose --profile load run --rm k6 run /scripts/<tên>.js`.

> ⚠️ **Không phải số liệu production.** k6 chạy **cùng Docker VM** với server: 8 vCPU chia cho postgres + redis + app + worker + caddy + chính bộ sinh tải, cộng overhead mạng của Docker Desktop trên macOS. Coi đây là **sàn**, không phải trần. Đo lại trên hạ tầng tách rời trước khi tin.

Lần đo này chạy trên **cơ sở dữ liệu trắng** vừa migrate, sau khi dọn toàn bộ dữ liệu thử. Bản đo trước (2026-08-07) chạy trên DB đã có sẵn ~108k bản ghi, nên hai bảng không so sánh trực tiếp được với nhau.

### Redirect — `load/redirect.js`

Mục tiêu: p99 < 80ms @ 500 RPS.

| Giai đoạn | p90 | p95 | **p99** | lỗi |
|---|---|---|---|---|
| burst 500 RPS × 60s | 1,7–1,9ms | 2,1–2,9ms | **4,3 – 9,9ms** | 0 / 30.001 |

**Dư 8–19 lần.** Tải gồm 2% code không tồn tại (mô phỏng dò quét) và 2% link hết hạn; negative cache khiến cả hai không chạm database.

### Về chênh lệch giữa các lần đo

Bản trước ghi "2,23ms → 7,15ms, không giải thích được". Đã chạy **7 lượt liên tiếp** (2026-08-09) để đo chính độ tản đó:

| | avg | median | p99 | max |
|---|---|---|---|---|
| khoảng quan sát | 0,78 – 1,33ms | **0,58 – 0,76ms** | **4,27 – 9,89ms** | 22,8 – 98,6ms |

**Trung vị gần như không đổi trong khi p99 xê dịch hơn hai lần.** Nếu đường xử lý redirect chậm đi thì trung vị phải chạy theo — nó không chạy. Nên chênh lệch 2,23 → 7,15 của bản trước **nằm gọn trong độ tản bình thường của môi trường này**, không phải một hồi quy.

Nguyên nhân của cái đuôi thì **vẫn chưa xác định**. Giả thuyết "do worker chạy nền" đã được kiểm và **bác bỏ**: dừng worker cho p99 6,01 và 6,64ms, còn lượt đối chứng bật worker lại cho **4,27ms — thấp nhất trong cả 7 lượt**. Lưu ý lượt đối chứng chạy sau cùng nên có thể hưởng lợi từ cache đã ấm; đó là điểm yếu của phép thử này.

Kết luận dùng được: **coi p99 redirect là "dưới 10ms trên máy này", đừng trích một con số lẻ.** Cần một con số chắc thì phải đo trên hạ tầng tách rời — vẫn là mục treo trong checklist go-live.

### Render biểu mẫu — `load/render.js`

Mục tiêu: p99 < 300ms @ 200 RPS.

| p90 | p95 | **p99** | lỗi |
|---|---|---|---|
| 4,11ms | 5,14ms | **12,4ms** | 0 / 4.501 |

> Lần đầu chạy lại kịch bản này tôi **quên truyền `FORM`**, nên nó gọi `fm_test` — một biểu mẫu không tồn tại — và nhận 404 suốt. Hai trong ba check vẫn **xanh**, vì chúng viết dạng `r.status !== 200 || …`: một check chỉ khẳng định điều gì đó *khi* request thành công sẽ báo pass cho lượt chạy không đo được gì. Chỉ có `http_req_failed = 100%` lộ ra. Truyền `-e FORM=$LOAD_FORM` mới là đo thật.

### Submit — `load/submit.js`

Đây là chỗ bản đo trước **sai**, và sai theo kiểu tệ nhất: nó công bố một con số của hệ thống không tồn tại.

**Với giới hạn mặc định đang ship**, k6 nhận **120 / 6.000** request thành công, phần còn lại là `429`. Đó không phải lỗi — đó là `PUBLIC_WRITE_IP_LIMIT = 60/phút` làm đúng việc: mọi request từ một container k6 dùng chung một dải /24, nên luật theo IP chạm trần trước ứng dụng rất xa. 60/phút × 2 phút = 120, khớp chính xác.

Nên phải nói rõ **con số nào đo dưới điều kiện nào**:

| Điều kiện | Trần quan sát được |
|---|---|
| Mặc định (`PUBLIC_WRITE_IP_LIMIT=60`) | **60 lượt gửi/phút mỗi dải /24** — đây là con số vận hành |
| Nâng giới hạn (`=1000000`) | xem bảng dưới — đây là năng lực ứng dụng |

Đo lại ngày 2026-08-09 (cột **p99 (08-09)**), trên DB đã có sẵn dữ liệu chứ không trắng — xem ghi chú bên dưới:

| RPS yêu cầu | p95 (08-08) | p99 (08-08) | **p99 (08-09)** | throughput thực (08-09) | ghi chú |
|---|---|---|---|---|---|
| 100 | 5,29ms | 10,95ms | **9,03ms** | 100/s | sạch, 0 lỗi |
| 300 | 3,91ms | 10,82ms | **8,67ms** | 300/s | sạch, 0 lỗi |
| 600 | 4,77ms | 15,45ms | **28,7ms** | 600/s | sạch, 0 lỗi |
| 900 | 47,3ms | 175ms | **379ms** | 887/s | 431 iteration bị bỏ |
| 1200 | 314ms | 428ms | **1,03s** | **598/s** | 35.811 iteration bị bỏ |

**Đầu gối vẫn nằm giữa 600 và 900/s** — hai lần đo cách nhau một ngày đồng ý ở điểm này.

Nhưng ở 1200 RPS hai lần đo **khác nhau về chất**: bản 08-08 vẫn đạt 1.120/s, bản 08-09 chỉ còn **598/s — thấp hơn cả lượt đo 600 RPS**. Qua đầu gối, hệ thống không chỉ chậm đi mà **ngừng tăng thông lượng**.

Điều kiện khác nhau và nó giải thích được phần lớn: các lượt đo hôm nay chạy nối tiếp nhau và **chính chúng bơm dữ liệu vào DB** — `forms.submissions` đi từ 6.149 lên **156.089 dòng (126 MB)** trong lúc đo. Bảng 08-08 chạy trên DB trắng. Vì vậy hai cột không phải hai lần đo cùng một hệ thống, và cột mới **gần với vận hành thật hơn** ở chỗ nó có dữ liệu sẵn.

Ở mọi mức, server **không trả một lỗi nào** — nó chậm lại và bỏ iteration ở phía sinh tải, chứ không sụp. So với burst thiết kế 50–100/s: dư 6–9 lần **nếu** giới hạn được nâng; với cấu hình mặc định thì giới hạn mới là thứ quyết định, không phải năng lực máy.

Advisory lock của audit chain (theo tenant) không thành nút cổ chai ở mức này — phần việc trong lock chỉ là một SELECT + một INSERT. Nhưng nó là trần **theo tenant**: một tổ chức duy nhất vượt xa mức này sẽ gặp nó trước.

### Export ở quy mô thật

181.060 submission do chính các lần chạy submit ở trên sinh ra, form 1 phiên bản, có một trường nhạy cảm được mã hoá riêng:

| | Thời gian | Kích thước | RAM worker |
|---|---|---|---|
| không kèm dữ liệu nhạy cảm | **8,4s** | 14,7 MB | ~114 MB |
| kèm dữ liệu nhạy cảm (giải mã từng dòng) | **21,3s** | 15,1 MB | ~114 MB |

Chênh 12,9s là chi phí mở khoá theo từng chủ thể — mỗi dòng là một lần unwrap. Nó **tuyến tính theo số dòng**, không theo kích thước tệp: hai bản chỉ chênh 0,4 MB.

RAM đứng yên ở ~114 MB cho cả hai, tức là đường ghi có stream thật chứ không dựng cả workbook trong bộ nhớ. Đây là thứ quyết định liệu một tổ chức có 5 triệu bản ghi thì export có giết worker hay không — và ở mức đo được thì không.

