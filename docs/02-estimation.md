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

## 2.6 Kết quả đo (2026-08-07)

Kịch bản trong `load/`, chạy bằng `docker compose --profile load run --rm k6 run /scripts/<tên>.js`.

> ⚠️ **Không phải số liệu production.** k6 chạy **cùng Docker VM** với server: 8 vCPU chia cho postgres + redis + app + worker + caddy + chính bộ sinh tải, cộng overhead mạng của Docker Desktop trên macOS. Coi đây là **sàn**, không phải trần. Đo lại trên hạ tầng tách rời trước khi tin.

### Redirect — `load/redirect.js`

Mục tiêu: p99 < 80ms @ 500 RPS.

| Giai đoạn | p90 | p95 | **p99** | lỗi |
|---|---|---|---|---|
| burst 500 RPS × 60s | 0,99ms | 1,33ms | **2,23ms** | 0 / 30.001 |

**Dư ~36 lần.** Tải gồm 2% code không tồn tại (mô phỏng dò quét) và 2% link hết hạn; negative cache khiến cả hai không chạm database. Một outlier `max = 8,16s` — request đầu tiên, cold connection; p99 không phản ánh nó.

### Render biểu mẫu — `load/render.js`

Mục tiêu: p99 < 300ms @ 200 RPS.

| p90 | p95 | **p99** | lỗi |
|---|---|---|---|
| 5,01ms | 6,58ms | **10,85ms** | 0 / 13.503 |

### Submit — `load/submit.js`

Mục tiêu: p99 < 500ms @ 100 RPS.

| RPS yêu cầu | p95 | **p99** | throughput thực | lỗi |
|---|---|---|---|---|
| 100 | 6,97ms | **16,78ms** | 100/s | 0 |
| 300 | 8,11ms | 84,53ms | 300/s | 0 |
| 600 | 11,21ms | 66,34ms | 599/s | 0 |
| 1200 | 535ms | 613ms | **802/s** | 0 |

**Trần ≈ 800 submission/s.** Ở 1200 RPS, k6 drop 23.568 iteration vì không sinh kịp; server xử lý hết những gì nhận được, **không lỗi nào**. Bão hòa sạch, không sụp đổ. So với burst thiết kế 50–100/s: **dư ~8 lần**.

Advisory lock của audit chain (theo tenant) **không** thành nút cổ chai ở mức này — phần việc trong lock chỉ là một SELECT + một INSERT. 108.386 entry, chain vẫn `valid: true` sau ~60k ghi đồng thời. Nhưng nó là trần **theo tenant**: một tổ chức duy nhất vượt xa mức này sẽ gặp nó trước.

### Export ở quy mô thật

108.382 submission, form 2 phiên bản, có trường nhạy cảm:

| | Thời gian | Kích thước | RAM worker |
|---|---|---|---|
| không kèm dữ liệu nhạy cảm | **2s** | — | ~37 MB |
| kèm dữ liệu nhạy cảm (giải mã từng dòng) | **14s** | 9,9 MB | ~70 MB |

RAM ~70 MB cho 108k dòng xác nhận StreamWriter hoạt động đúng: dựng workbook trong bộ nhớ sẽ tốn hơn hàng chục lần.

### Bug tìm được nhờ load test

**Truy vấn export là O(n×m) và chỉ lộ ra trên 1.000 dòng.** Subquery lấy trạng thái đồng ý join `consent.current_consents` mà **thiếu `tenant_id`** — cột dẫn đầu khóa chính của bảng đó. Index vô dụng, Postgres quét tuần tự toàn bộ bảng cho **mỗi** submission:

```
Seq Scan on current_consents  (rows=216651, loops=2000)
Buffers: shared hit=5,820,000
Execution Time: 45301 ms        -- cho 2.000 dòng
```

Ở 108k dòng là ~23 tỉ phép so sánh; job chạy 1.050 giây và chưa kết thúc.

Sửa: đọc thẳng từ `consent.records` — nơi giữ **điều đã đồng ý cùng submission đó** — thay vì join sang trạng thái hiện tại. Nhanh hơn **và** đúng ngữ nghĩa hơn cho một báo cáo.

```
Execution Time: 45301 ms → 93 ms      (485×)
Export 108k dòng: >1050s → 2s
```

Với 5 bản ghi thử nghiệm, truy vấn này chạy tức thì. Không có load test thì bug này chỉ lộ ra ở khách hàng đầu tiên có dữ liệu thật.

**Bug thứ hai, trong chính bộ test:** lần chạy đầu báo `http_req_failed 3,80%`. Không phải hệ thống — k6 mặc định coi 404/410 là thất bại, mà đó chính là hai trường hợp kịch bản cố ý sinh ra. Thêm `http.setResponseCallback(http.expectedStatuses(302, 404, 410))`. Nếu không sửa, bộ test báo động giả mãi mãi và người ta học cách phớt lờ nó.

### Đã sửa sau đợt đo (2026-08-07)

**Phễu chỉ nối được 2/4 chặng.** `analytics.events` chỉ có `click` và `submit`; `views` và `starts` luôn bằng 0, nên `Tỉ lệ hoàn thành` = submits/views **luôn in ra 0,0%** cho một form có 108k lượt gửi, và sheet "Rơi rớt theo trang" luôn rỗng. Cùng loại với bug query O(n×m): một con số sai được in ra và trông như thật.

Sửa theo hai hướng khác nhau, có chủ đích:

- `form_view` ghi **ở server** khi render `GET /api/pub/forms/{id}`. Mẫu số của tỉ lệ hoàn thành không nên phụ thuộc việc client có chạy JavaScript hay không — nếu phụ thuộc, sẽ có lúc thiếu, và biểu hiện là tỉ lệ chuyển đổi vượt 100%.
- `form_start` và `form_page_view` qua `POST /api/pub/events` — đây là tín hiệu tương tác, chỉ client biết.

Endpoint beacon **chỉ nhận hai loại đó**. `click` và `submit` do server ghi; chấp nhận chúng từ client sẽ cho phép bất kỳ ai thổi phồng số liệu chuyển đổi của tenant khác bằng một vòng lặp curl.

### Việc còn lại

- Đo lại trên hạ tầng tách rời (client và server khác máy).
- Chưa đo: upload tệp ở tải cao, cổng DSR, đăng nhập (argon2id ~24ms/lần là chi phí có chủ đích).
- Theo dõi các ngưỡng ở [7.4](07-operations.md#74-scaling-path) hằng tháng: scaling path chỉ hoạt động nếu có người nhìn những con số đã kích hoạt nó.
