# 7. Vận hành

## 7.1 SLO — định nghĩa trước, alert sau

| # | SLO | Đo bằng |
|---|---|---|
| 1 | 99,5% redirect trả về < 80ms trong 30 ngày | histogram `http_request_duration{route="/r/:code"}` |
| 2 | 99,9% submission được ghi bền vững (không mất) | `submissions_created_total` vs `submission_errors_total` |
| 3 | **100% DSR request được xử lý trước `due_at`** | `dsr_overdue_count` |

SLO 3 là SLO **pháp lý**, không phải kỹ thuật — và là cái duy nhất mà việc vi phạm dẫn tới hậu quả ngoài phạm vi hệ thống.

## 7.2 Metrics + ngưỡng alert

### RED — theo endpoint hot path

| Metric | Ngưỡng cảnh báo | Mức độ |
|---|---|---|
| `redirect_p99` | > 150ms trong 5 phút | 🔴 page |
| `redirect_error_rate` (5xx) | > 1% trong 5 phút | 🔴 page |
| `submission_p99` | > 1s trong 5 phút | 🟡 ticket |
| `submission_error_rate` (5xx) | > 0,5% trong 5 phút | 🔴 page |
| `form_render_p99` | > 600ms trong 10 phút | 🟡 |

### USE — theo tài nguyên

| Metric | Ngưỡng | Mức độ |
|---|---|---|
| Postgres connection pool utilization | > 80% trong 5 phút | 🟡 |
| Postgres disk | > 80% | 🟡 · > 90% 🔴 |
| **Disk `/data/files`** | > 75% | 🟡 — đây là ngưỡng chạm trước nhất, xem [7.4](#74-scaling-path) |
| Redis memory | > 80% maxmemory | 🟡 |
| Queue depth (`core.outbox` chưa gửi) | > 5.000 | 🟡 |
| **Queue time-to-drain** | > 5 phút | 🔴 |
| Redis Stream lag (`ev`) | > 60s | 🟡 |

> Queue depth một mình thì nói dối: 10k message rút hết trong 30 giây là bình thường; 100 message tăng đều mãi là sự cố. **Luôn alert theo time-to-drain.**

### Metrics nghiệp vụ — lớp bắt được "hệ thống xanh nhưng sản phẩm hỏng"

| Metric | Ngưỡng | Vì sao quan trọng |
|---|---|---|
| **`dsr_overdue_count`** | **> 0** → 🔴 page ngay | Vi phạm nghĩa vụ pháp lý. Alert nghiêm trọng nhất hệ thống |
| `dsr_due_within_24h` | > 0 → 🟡 nhắc | Cảnh báo trước vách, không phải sau |
| `submissions_created_total` | giảm > 50% so với cùng giờ tuần trước | Form hỏng nhưng vẫn trả 200 |
| `consent_records_written_total` | **≠** số submission có consent | 🔴 — bất biến pháp lý bị gãy |
| `funnel_conversion_rate` | tụt > 30% đột ngột | Rẽ nhánh hỏng sau khi publish version mới |
| `analytics_events_dropped_total` | > 0 kéo dài | Redis hoặc buffer có vấn đề |
| `audit_chain_verification_failed` | > 0 → 🔴 | Log bị can thiệp |
| `orphan_files_swept` | > 100/ngày | Client bỏ dở upload bất thường |
| `cache_hit_ratio` (redirect) | < 80% trong 10 phút | Hot-key hoặc lỗi invalidation |
| `form_publish_blocked_total` | theo lý do | Cho biết người dùng vấp ở đâu trong builder |

### Logging

- JSON có cấu trúc, một sự kiện một dòng, luôn mang `trace_id` — xuyên suốt qua queue và worker.
- **Không bao giờ log**: `answers`, `evidence`, email/phone đầy đủ, token, `TENANT_KEK`. Log `data_subject_id` (đã pseudonymised) là đủ để lần vết.
- Bên trong monolith, `trace_id` + log tốt lo được ~90% nhu cầu. **Chỉ dựng OpenTelemetry khi request thực sự đi qua ranh giới service** — đúng nguyên tắc monolith-first áp cho observability.

### Đã hiện thực

`GET /metrics` (Prometheus). Nhãn RED lấy từ **pattern route đã khớp** (`r.Pattern`), không phải path — dùng path sẽ sinh một time series cho mỗi short code và biến endpoint metrics thành rò rỉ bộ nhớ.

Gauge đọc từ DB được **làm mới theo timer 30s**, không đọc lúc scrape: một lần scrape chạy chín truy vấn trên database đang tải sẽ biến việc giám sát thành nguồn tải chứ không phải góc nhìn vào tải. Khi truy vấn lỗi, giá trị cũ được giữ lại thay vì ghi 0 — trên đúng cái gauge mà 0 nghĩa là "không có gì quá hạn".

| Nhóm | Metric |
|---|---|
| RED | `collectr_http_request_duration_seconds{route,method,status}` · `collectr_http_requests_total{route,method,status_class}` |
| Nghiệp vụ | `collectr_dsr_overdue_count` · `collectr_dsr_due_within_24h` · `collectr_submissions_total{outcome}` · `collectr_consent_records_written_total` · `collectr_analytics_events_dropped_total` · `collectr_link_cache_lookups_total{result}` · `collectr_audit_chain_verification_failed_total` · `collectr_form_publish_blocked_total{reason}` · `collectr_exports_total{outcome,sensitive}` · `collectr_webhook_deliveries_total{outcome}` · `collectr_rate_limited_total{rule}` |
| Hàng đợi | `collectr_outbox_pending` · `collectr_outbox_oldest_seconds` · `collectr_webhook_deliveries_pending` · `collectr_exports_queued` |
| USE | `collectr_db_pool_in_use` · `collectr_db_pool_total` · Go runtime + process |

Histogram dùng bucket đặt quanh chính các mục tiêu thiết kế (80ms · 300ms · 500ms). Bucket mặc định gộp cả ba vào một bin và làm mọi ngưỡng p99 ở trên không đo được.

**`/metrics` phải đặt sau reverse proxy hoặc firewall.** Không phải dữ liệu cá nhân, nhưng nó mô tả hình dạng của deployment chi tiết hơn mức người lạ cần biết.

### Rate limit endpoint công khai

| Endpoint | Giới hạn | Khi Redis chết |
|---|---|---|
| `POST /submissions`, `POST /uploads` | 60/phút/dải IP · 600/phút/form | **Fail open** |
| `POST /dsr/identify` | 3/giờ/định danh (đếm trong Postgres) | **Fail closed** |
| `POST /auth/login` | 10/15 phút/email | **Fail closed** |
| `POST /password/forgot` | 3/giờ/email | **Fail closed** |

Hai hướng ngược nhau, có chủ đích: từ chối một submission là **mất vĩnh viễn** câu trả lời của một khách hàng, còn để lọt spam trong lúc Redis chết chỉ là phiền toái trong một sự cố đã có người trực. Ngược lại, `/identify` không giới hạn là một oracle cho biết doanh nghiệp đang giữ dữ liệu của những ai — thà tạm không phục vụ.

Hai tầng giới hạn cho endpoint ghi: theo dải IP chặn **một người** flood, theo form chặn **một biểu mẫu** bị flood từ khắp nơi — tầng thứ hai là thứ mà giới hạn theo IP bỏ sót hoàn toàn. Khóa theo `/24` chứ không theo IP đơn: một văn phòng sau NAT dùng chung địa chỉ.

### Con trỏ rollup

`analytics.rollup_state` giữ mốc gom cuối cùng. Trước đây con trỏ chỉ nằm trong bộ nhớ và khởi tạo lại về `now - 1 giờ` mỗi lần worker chạy: bất kỳ lần chết nào lâu hơn một tiếng đều để lại một lỗ hổng **không bao giờ được lấp**. Sự kiện thô vẫn còn, job vẫn báo thành công, và số lượt bấm chỉ đơn giản là thấp hơn thực tế. Trên deployment này chênh 39 so với 161.

Gom theo **khoảng**, không theo từng bucket: bắt kịp 90 ngày tồn đọng theo bucket 5 phút là ~26.000 lượt round trip, chậm đến mức trên thực tế việc bắt kịp không bao giờ xảy ra. Mỗi tick bắt tối đa 24 giờ để một lần chạy không giữ khóa trên bảng rollup quá lâu.

Cần theo dõi: `now() - cursor` từ `analytics.rollup_state`. Bình thường dưới 6 phút. Vượt 1 giờ nghĩa là worker chết hoặc job đang lỗi.

### Nhiều tên miền

`links.domains` là bảng per-tenant, nhiều dòng, có một dòng `is_default`. `links.resolve(host, code)` phân giải theo **Host header**, nên hai tên miền chạy trên cùng một deployment mà không cần gì thêm ở tầng ứng dụng.

| | Tên miền | Lấy từ |
|---|---|---|
| Trang biểu mẫu, cổng DSR, API quản trị | `form.example.com` | `BASE_URL` |
| Link rút gọn, QR | `rutgon.example.com` | dòng `links.domains` của **chính link đó** |

Điểm mấu chốt: URL rút gọn dựng từ tên miền của link, **không phải** từ host của request hỏi nó. Nếu dựng theo request, quản trị viên mở bảng điều khiển trên `form.example.com` sẽ nhận về QR trỏ vào `form.example.com` — và QR thì đã in lên tờ rơi, không sửa lại được.

Đổi tên miền mặc định chỉ ảnh hưởng link **tạo mới**. Link cũ giữ nguyên tên miền của chúng, vì mã của chúng đã ở ngoài đời. Cùng lý do, xóa một tên miền còn link sẽ bị từ chối (409) thay vì xóa lan.

Vận hành: thêm cả hai host vào `SITE_ADDRESS` để Caddy cấp chứng chỉ cho từng cái, và trỏ DNS. `deploy/Caddyfile` có sẵn khối cấu hình để **giới hạn host rút gọn chỉ phục vụ `/r/` và `/q/`** — nên dùng, vì đó là tên miền phát cho người lạ.

### Bộ khởi đầu

Prometheus + Grafana + `/metrics` là đủ, đóng gói sẵn trong `docker-compose.observability.yml` (profile tùy chọn). Không ép ai chạy Loki + Tempo cho một instance 200 workspace — vận hành bộ quan sát tốn hơn vận hành ứng dụng là một thất bại thiết kế.

## 7.3 Backup / restore / migration

### Backup

| Đối tượng | Cách | Tần suất | Giữ |
|---|---|---|---|
| PostgreSQL | `pg_basebackup` + WAL archiving (PITR) | full hằng ngày, WAL liên tục | **30 ngày** — con số này là cam kết pháp lý, phải ghi trong chính sách quyền riêng tư |
| `/data/files` | `restic`/`rclone` tới đích ngoài máy | hằng ngày, incremental | 30 ngày |
| **`TENANT_KEK`** | **Thủ công, ngoài băng, TÁCH KHỎI backup DB** | khi tạo/xoay | vĩnh viễn |
| Audit checkpoint đã ký | ghi ra ngoài DB | hằng ngày | vĩnh viễn |

> ⚠️ **Mất `TENANT_KEK` = mất vĩnh viễn mọi field nhạy cảm và mọi file đã mã hóa.** Không có đường khôi phục — đó chính là thuộc tính khiến crypto-shredding hoạt động. Backup KEK phải nằm ở nơi khác backup DB, nếu không cả hai cùng mất trong một sự cố và tính năng "xóa triệt để" trở thành "mất trắng".

### Restore

- **Diễn tập restore hằng tháng** trên môi trường riêng, bấm giờ. Backup chưa từng restore không phải backup.
- Mục tiêu: RTO 4h, RPO 15 phút (nhờ WAL archiving).
- Sau khi restore: chạy `POST /api/v1/audit/verify` để xác nhận hash chain nguyên vẹn.
- **Bản restore vẫn phải tôn trọng lệnh xóa đã thực hiện**: dữ liệu nhạy cảm đã crypto-shred sẽ không giải mã được (đúng ý đồ). Với dữ liệu không mã hóa đã hard delete, chạy job `reapply-erasures` đối chiếu `consent.dsr_requests` type `erase` đã `fulfilled` — **bước này bắt buộc, thiếu nó thì restore = khôi phục dữ liệu mà chủ thể đã yêu cầu xóa.**

### Migration

- `goose` hoặc `atlas`, migration versioned trong git, chạy tự động lúc app khởi động (có advisory lock chống đua giữa nhiều instance).
- **Expand–contract** cho mọi thay đổi phá vỡ: thêm cột nullable → backfill → chuyển đọc/ghi → xóa cột cũ ở release sau. Không downtime.
- Với `analytics.events` đã partition: tạo partition trước 7 ngày bằng job, không để tới lúc insert mới phát hiện thiếu.
- Migration đụng `audit.entries` phải review đặc biệt — mọi thay đổi cấu trúc đều có nguy cơ làm gãy hash chain. Thêm cột thì canonical JSON phải giữ nguyên tập trường cũ.

## 7.4 Scaling path

Mỗi bước có **điều kiện kích hoạt định lượng**. Hệ thống lớn lên theo nhu cầu, không lớn trước.

| Khi metric X vượt Y | Nâng lên Z | Ghi chú |
|---|---|---|
| Disk `/data/files` > 75% **hoặc** dung lượng file > 300 GB | `STORAGE_DRIVER=s3` + MinIO container (hoặc S3 ngoài) | Job migrate copy + verify checksum. Contract không đổi → 0 dòng code sửa |
| Cần chạy **> 1 app instance** (bất kể lý do) | Bắt buộc `STORAGE_DRIVER=s3` | Local disk không chia sẻ được. Ngưỡng cứng nhất |
| Redirect > **1.000 RPS** steady | Tách Postgres sang máy riêng, thêm read replica cho grid/analytics | Redirect vẫn đọc primary qua Redis |
| Redirect > **5.000 RPS** | In-process LRU (otter) 10k entry TTL 10s trước Redis | Cắt round-trip Redis cho hot key |
| Postgres CPU > 70% kéo dài | Read replica; route grid + funnel + export sang replica | Submit/consent vẫn ở primary (cần strong consistency) |
| `analytics.events` > **500 GB** hoặc funnel query > 2s | Rút gọn retention raw còn 30 ngày; nếu vẫn không đủ → ClickHouse chỉ cho analytics | Rollup đã chịu được phần lớn tải |
| Queue time-to-drain > 5 phút thường xuyên | Tăng số worker; nếu vẫn nghẽn → tách queue theo tenant | Postgres queue chịu ~1–2k msg/s |
| DB > **2 TB** | Partition `submissions` theo tháng; archive form đã đóng sang cold storage | Sharding là phương án cuối, chưa cần bàn |
| Nhiều đội deploy độc lập **và** đã đo được profile tải phân kỳ | Strangler fig: rút `analytics` ra trước, rồi `consent` | `contracts/` + phân tách schema đã dọn sẵn đường. **Không làm nếu chỉ vì "kiến trúc đẹp hơn"** |

### Thứ tự rút module (nếu tới lúc đó)

1. **`analytics`** — phụ thuộc lỏng nhất, chỉ tiêu thụ event, chấp nhận eventual consistency. Rủi ro thấp nhất.
2. **`consent`** — biên giới rõ nhất, nhưng phải giữ tính atomic với submission → cần saga hoặc outbox có bù trừ. **Chỉ làm khi có nhu cầu thật sự** (ví dụ dùng chung consent cho nhiều sản phẩm), không phải vì tải.
3. `links` (redirect) — chỉ khi profile tải redirect và admin phân kỳ đã đo được, đúng tiêu chí monolith-first.

`forms` + `submissions` là lõi, không tách.

## 7.5 Checklist trước khi go-live

**Khởi tạo lần đầu**
- [ ] Đọc mã khởi tạo từ log và tạo chủ sở hữu **ngay sau khi khởi động**, không để deployment nằm không chủ qua đêm
- [ ] Xác nhận `docker compose logs collectr | grep setup_token` không còn ra gì sau khi đã có chủ sở hữu
- [ ] Nếu đặt `SETUP_TOKEN` thủ công: xoá khỏi `.env` sau khi tạo xong, nó không còn tác dụng gì ngoài việc nằm đó

Một cài đặt mới chưa có ai sở hữu. Instance nằm sau TLS tự động xuất hiện trong Certificate Transparency log trong vài giây, và đó là nơi máy quét đọc — nên khoảng thời gian giữa `docker compose up` và lần tạo tài khoản đầu tiên là khoảng thời gian duy nhất mà mã khởi tạo là thứ đứng giữa.

**Kỹ thuật**
- [x] Load-test `/r/{code}` (k6) → p99 **4,3–9,9ms** ở 500 RPS qua 7 lượt ([kết quả](02-estimation.md#26-kết-quả-đo-2026-08-08))
- [x] Load-test `POST /submissions` → đầu gối giữa 600–900/s **khi đã nâng giới hạn**; với cấu hình mặc định thì `PUBLIC_WRITE_IP_LIMIT` là thứ chặn trước, không phải máy
- [x] Xác minh chuỗi audit 181.065 entry → `valid: true` trong 0,29s
- [ ] Quyết định `PUBLIC_WRITE_IP_LIMIT` cho tình huống thật: 60/phút mỗi dải /24 là đúng cho form trên website, **quá thấp** cho gian hàng hội chợ nơi mọi người đi chung một NAT
- [ ] Đo lại trên hạ tầng tách rời (client và server khác máy)
- [ ] Load-test upload tệp 1 MB ở tải cao
- [ ] Diễn tập restore thành công, có bấm giờ
- [ ] `TENANT_KEK` đã backup **ngoài** backup DB, đã kiểm tra khôi phục được
- [ ] `govulncheck` sạch, base image distroless đã pin digest
- [ ] Alert `dsr_overdue_count > 0` đã đấu vào kênh trực

**Tuân thủ**
- [ ] Văn bản thông báo/đồng ý đã có luật sư duyệt, publish version 1
- [ ] `DSR_SLA_HOURS` đã đặt đúng theo tư vấn pháp lý
- [ ] Chính sách retention mặc định đã đặt và ghi trong chính sách quyền riêng tư
- [ ] Backup retention 30 ngày đã nêu rõ với chủ thể dữ liệu
- [ ] Xác nhận không có dữ liệu cá nhân rời khỏi hạ tầng (không CDN, không font external, không telemetry)
- [ ] Quy trình xử lý sự cố rò rỉ dữ liệu (ai thông báo, cho ai, trong bao lâu) đã có văn bản
- [ ] Hồ sơ đánh giá tác động xử lý dữ liệu cá nhân đã lập (ngoài scope hệ thống — cần làm thủ công ở MVP)

## 7.8 Cấu hình & triển khai

Chuyển từ README sang đây khi README dài quá 300 dòng: đây là tài liệu người vận
hành mở khi đang dựng hệ thống, không phải thứ người ta đọc để quyết định có thử
sản phẩm hay không.

### Cấu hình

| Biến môi trường | Mặc định | Ý nghĩa |
|---|---|---|
| `BASE_URL` | `http://localhost:8080` | Origin của ứng dụng: trang biểu mẫu, cổng DSR, API quản trị |
| `SHORT_URL_BASE` | `BASE_URL` | Origin của link rút gọn. Đặt riêng để chạy shortener trên tên miền của nó |
| `DATABASE_URL` | — | Chuỗi kết nối PostgreSQL |
| `REDIS_URL` | — | Chuỗi kết nối Redis |
| `TENANT_KEK` | — | **Bắt buộc.** Khóa gốc mã hóa, 32 byte base64 |
| `SETUP_TOKEN` | tự sinh | Mã cho endpoint tạo chủ sở hữu đầu tiên. Bỏ trống thì máy chủ tự sinh và in ra log |
| `PUBLIC_WRITE_IP_LIMIT` | `60` | Lượt gửi/tải tệp mỗi phút cho mỗi dải /24. Nâng lên khi khách đi chung một NAT (hội chợ, văn phòng) |
| `PUBLIC_WRITE_FORM_LIMIT` | `600` | Lượt gửi mỗi phút cho mỗi biểu mẫu |
| `STORAGE_DRIVER` | `local` | `local` (đĩa) hoặc `s3`. **Mặc định là đĩa** — MinIO không chạy trừ khi bạn bật profile `s3` |
| `STORAGE_S3_ENDPOINT` | — | `minio:9000`, `<acc>.r2.cloudflarestorage.com`, `s3.cloud.fpt.vn`, `s3.amazonaws.com` |
| `STORAGE_S3_BUCKET` · `_ACCESS_KEY` · `_SECRET_KEY` | — | Bắt buộc khi `STORAGE_DRIVER=s3` |

Tệp đính kèm được **mã hoá trước khi rời khỏi tiến trình**, bằng khoá riêng của từng chủ thể dữ liệu. Bucket chỉ chứa ciphertext — nhà cung cấp, một bucket policy đặt sai, hay một bản sao lưu bị chép đi đều chỉ thu được byte không ai đọc được. Đó cũng là lý do link tải **không** phải presigned URL của S3: trình duyệt nhận về sẽ là bản mã. Ứng dụng phục vụ byte, vì nó là chỗ giữ khoá, kiểm quyền và ghi nhật ký.

Thử bằng MinIO tại chỗ trước khi trỏ ra cloud:

```bash
docker compose --profile s3 up -d minio
STORAGE_DRIVER=s3 STORAGE_S3_ENDPOINT=http://minio:9000 \
STORAGE_S3_BUCKET=collectr STORAGE_S3_ACCESS_KEY=... STORAGE_S3_SECRET_KEY=... \
  docker compose up -d collectr worker
```
| `STORAGE_DRIVER` | `local` | `local` (đĩa) hoặc `s3`. **Mặc định là đĩa** — MinIO không chạy trừ khi bạn bật profile `s3` |
| `STORAGE_LOCAL_PATH` | `/data/files` | Thư mục lưu tệp khi dùng `local` |
| `STORAGE_S3_*` | — | Endpoint, bucket, khóa truy cập khi dùng `s3` |
| `DSR_SLA_HOURS` | `72` | Hạn xử lý yêu cầu của chủ thể dữ liệu |
| `DEFAULT_RETENTION_DAYS` | `730` | Thời hạn lưu mặc định |
| `RAW_EVENT_RETENTION_DAYS` | `90` | Thời hạn lưu sự kiện thô |
| `DEPLOYMENT_ROLE` | `controller` | `controller` (tự dùng) hoặc `processor` (chạy dịch vụ cho bên khác) |

| `SMTP_HOST` `SMTP_FROM` | — | Bắt buộc để mời đồng nghiệp và để chủ thể dữ liệu nhận được liên kết truy cập |

> [!WARNING]
> Chưa cấu hình SMTP thì lời mời và liên kết truy cập được **ghi ra log thay vì gửi đi**. Dùng để thử được, **không dùng thật được** — người khác không đọc được log của bạn.
>
> Thử tại chỗ: `docker compose --profile dev up -d mailpit`, đặt `SMTP_HOST=mailpit` `SMTP_PORT=1025` `SMTP_STARTTLS=false`, rồi đọc mail ở http://localhost:8025

Danh sách đầy đủ: [`.env.example`](.env.example).

### Đặt sau proxy khác (Cloudflare, nginx, tunnel)

Collectr đọc địa chỉ khách từ `X-Forwarded-For`, lùi lại `TRUSTED_PROXY_HOPS` chặng. Đặt sai thì **không có lỗi nào cả** — địa chỉ sai lặng lẽ đi vào bản ghi đồng ý (bằng chứng ai đã đồng ý, từ đâu) và thành khoá của giới hạn theo IP, khiến mọi khách qua cùng một proxy dùng chung một hạn mức 60/phút.

| Cách triển khai | `TRUSTED_PROXY_HOPS` | Ghi chú |
|---|---|---|
| Chỉ Caddy (mặc định) | `1` | Không cần đổi gì |
| Cloudflare / nginx → Caddy → app | `2` | Caddy nối thêm một chặng của chính nó |
| Tunnel hoặc nginx → thẳng app, bỏ Caddy | `1` | Mất HSTS, cái còn lại app tự đặt |
| Chạy binary trần ra mạng | `0` | Bỏ qua `X-Forwarded-For` hoàn toàn |

Với bất cứ thứ gì đứng trước Caddy, `deploy/Caddyfile` phải có `trusted_proxies private_ranges` — nếu không Caddy **thay** header bằng địa chỉ nó thấy và IP khách mất trước khi tới app. Thiếu một trong hai vế còn tệ hơn thiếu cả hai: `hops=2` với header một entry sẽ trỏ ra ngoài mảng rồi rơi về địa chỉ socket, im lặng.

**Sau tunnel, Caddy không xin được chứng chỉ công cộng.** Thử thách ACME đi tới Cloudflare chứ không tới origin, nên Caddy thất bại rồi ngừng phục vụ 443 — triệu chứng là `502` từ tunnel. Thêm `tls internal` vào khối site: tunnel đã lo TLS công cộng rồi, đoạn origin chỉ cần mã hoá chứ không cần chứng chỉ ai cũng tin.

**`SITE_ADDRESS` phải đúng hostname khách thật sự truy cập.** Caddy khớp site theo Host; Host không khớp site nào thì nó trả **200 với thân rỗng** — không phải 404. Trang trắng, log toàn 200. Nếu các trang trống rỗng, kiểm chỗ này trước.

Đừng suy luận, hãy đo: mở **Cài đặt tổ chức** từ máy của bạn và so dòng *"IP của bạn như hệ thống ghi nhận"* với IP thật. Khớp là đúng; ra địa chỉ proxy hay dải nội bộ là sai.

> [!WARNING]
> `make secrets` sinh ra `TENANT_KEK` trong `.env`. **Sao lưu khóa này ở nơi tách biệt với bản sao lưu cơ sở dữ liệu.** Mất nó là mất vĩnh viễn mọi trường nhạy cảm và mọi tệp đã mã hóa — không có đường khôi phục. Đó chính là thuộc tính khiến tính năng xóa triệt để hoạt động.

### Chuyển sang object storage

Khi dung lượng tệp vượt ~300 GB, hoặc khi chạy nhiều hơn một instance:

```bash
STORAGE_DRIVER=s3
STORAGE_S3_ENDPOINT=http://minio:9000
STORAGE_S3_BUCKET=collectr
```
```bash
docker compose --profile s3 up -d      # kèm MinIO
```

Không cần sửa dòng code nào — cả hai driver dùng chung một giao diện `Storage`. Job chép dữ liệu từ `local` sang `s3` (kèm đối chiếu checksum) sẽ có ở v0.2.

