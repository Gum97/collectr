# 1. Yêu cầu

## 1.1 Chức năng lõi (chốt scope MVP)

| # | Module | Phạm vi |
|---|---|---|
| 1 | **Links** | short code, custom alias, expiry, redirect; dùng độc lập hoặc gắn form |
| 2 | **Forms** | builder 7 loại field (text, choice, multi-choice, rating, date, dropdown, file), conditional logic / rẽ nhánh, schema có version, submission grid |
| 3 | **QR** | sinh QR PNG/SVG cho mọi link (form cũng là một link) |
| 4 | **Consent & DSR** | consent record có bằng chứng, rút đồng ý, self-service tra cứu–sửa–yêu cầu xóa, retention per form, audit log bất biến, đánh dấu field nhạy cảm |
| 5 | **Analytics** | funnel click → form_view → submit, xử lý async |

Hai module nền bắt buộc: **IAM/workspace** (multi-tenant + RBAC) và **Files** (upload cho field type `file`).

### Cắt khỏi MVP — ghi rõ để không trôi scope

A/B test link · webhook out / CRM sync · email marketing · form đa ngôn ngữ · sinh Hồ sơ đánh giá tác động (DPIA) tự động · thanh toán · SSO cấp tenant · custom domain có ACME tự động (MVP: 1 domain cấu hình sẵn + wildcard).

## 1.2 Yêu cầu phi chức năng

| Thuộc tính | Mục tiêu | Ghi chú |
|---|---|---|
| Tải | < 100 RPS steady; **burst 300–500 RPS trong 1–2 phút** | Burst (quét QR tại hội nghị / điểm bán) mới là ràng buộc thật, không phải RPS trung bình |
| Latency | redirect p99 < 80ms · render form p99 < 300ms · submit p99 < 500ms · grid 10k dòng < 1s | |
| Availability | 99.5% (self-host 1 node) | Redirect chết = mọi link chết |
| Consistency | **Strong** cho submission + consent (cùng transaction). **Eventual ≤ 60s** cho analytics | Phân tuyến rõ ràng, xem dưới |
| Durability | submission / consent / audit: **zero loss**. Analytics: **best-effort, được phép mất** | |
| Retention | cấu hình per form; mặc định hệ thống 24 tháng; raw event 90 ngày | |
| Deploy | `docker-compose up -d`, ≤ 4 container, không phụ thuộc cloud | |

> **Phân tuyến mức đảm bảo** là quyết định phi chức năng quan trọng nhất của hệ thống này. Rất nhiều thiết kế bôi đều một mức độ đảm bảo lên cả hai loại dữ liệu và trả giá bằng latency (nếu chọn mức cao) hoặc bằng vi phạm pháp luật (nếu chọn mức thấp).

## 1.3 Giả định định lượng

Không có số liệu từ người dùng → nêu giả định rõ ràng trước khi thiết kế.

Kịch bản chuẩn: **một instance self-host phục vụ ~200 workspace** (mô hình agency/ISV hosting cho nhiều khách hàng). Mỗi workspace ~10 form đang chạy + ~50 link.

Nếu là một doanh nghiệp tự host cho riêng mình, mọi con số chia cho ~50 và kết luận không đổi (vẫn small scale) — nên thiết kế lấy kịch bản lớn hơn làm chuẩn.

## 1.4 Ranh giới trách nhiệm pháp lý → ràng buộc kiến trúc

| Mô hình triển khai | Vai trò theo luật | Hệ quả kiến trúc |
|---|---|---|
| Doanh nghiệp tự host | Workspace owner = **Bên Kiểm soát dữ liệu**. Collectr là công cụ | Mặc định |
| Ai đó chạy SaaS trên codebase này | Họ = **Bên Xử lý dữ liệu** | Bật `DEPLOYMENT_ROLE=processor`: khóa quyền đọc plaintext của operator, ghi audit mọi truy cập của admin hệ thống, bắt buộc cấu hình DPA per tenant |

**Ràng buộc suy ra, áp dụng cho mọi mô hình:**

1. **Không có dữ liệu cá nhân rời khỏi hạ tầng của tenant.** Không telemetry ra ngoài, không CDN bên thứ ba, không font/script external trong trang form public. Điều này đồng thời né luôn nghĩa vụ về chuyển dữ liệu xuyên biên giới.
2. **Không tồn tại submission thiếu consent record.** Ràng buộc atomic này quyết định lựa chọn DB ở [bước 4](04-data-model.md).
3. **Mọi thao tác chạm dữ liệu cá nhân đều để lại vết bất biến** — kể cả thao tác đọc hàng loạt (export).
4. **Mọi mốc thời gian tuân thủ là cấu hình.** `DSR_SLA_HOURS`, `BREACH_NOTIFY_HOURS`, `DEFAULT_RETENTION_DAYS` nằm trong env, không nằm trong code.
5. **Chính hệ thống tracking cũng phải giảm thiểu dữ liệu.** Không lưu IP đầy đủ, không cookie bên thứ ba, `visit_id` sống 30 phút và không nối được giữa các workspace. Thu hẹp phạm vi "dữ liệu cá nhân" mà nền tảng tự tạo ra rẻ hơn nhiều so với việc phải xin đồng ý cho chính tracking của mình.

## 1.5 Quyền chủ thể dữ liệu phải hỗ trợ

| Quyền | Hiện thực trong MVP |
|---|---|
| Được biết | Consent block hiển thị mục đích + bên kiểm soát + quyền, lấy từ văn bản có version |
| Đồng ý / không đồng ý | Checkbox riêng cho **từng mục đích**; im lặng ≠ đồng ý (không tick sẵn) |
| Truy cập | Self-service qua magic link, `GET /api/dsr/me/submissions` |
| Chỉnh sửa | `PATCH` submission, giữ revision cũ |
| Rút đồng ý | Append record `withdrawn`, không sửa record cũ; không ảnh hưởng tính hợp pháp của xử lý trước đó |
| Xóa | DSR request → xóa cứng + crypto-shred, có SLA |
| Hạn chế xử lý | `submissions.status = 'restricted'` → loại khỏi export/analytics |
| Phản đối | Cùng cơ chế DSR request, xử lý thủ công bởi workspace admin |

Quyền khiếu nại/tố cáo/khởi kiện/bồi thường là quy trình ngoài hệ thống — Collectr chỉ cung cấp **bằng chứng** (consent record + audit log) để phục vụ các quy trình đó.
