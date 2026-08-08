# Chính sách bảo mật

## Báo cáo lỗ hổng

**Xin đừng mở issue công khai cho lỗ hổng bảo mật.**

Gửi báo cáo qua GitHub Security Advisory (`Security` → `Report a vulnerability`) hoặc email `security@<domain>`.

Vui lòng kèm: mô tả lỗ hổng, các bước tái hiện, tác động bạn đánh giá, và phiên bản/cấu hình liên quan.

| Mốc | Cam kết |
|---|---|
| Xác nhận đã nhận | trong 48 giờ |
| Đánh giá ban đầu | trong 7 ngày |
| Bản vá cho lỗi nghiêm trọng | mục tiêu 30 ngày |
| Công bố | sau khi có bản vá, có ghi công nếu bạn muốn |

Chúng tôi không có chương trình thưởng lỗi bằng tiền, nhưng ghi nhận công khai mọi báo cáo hợp lệ.

## Phạm vi

**Trong phạm vi** — mã nguồn Collectr, cấu hình mặc định, Docker image chính thức, tài liệu triển khai.

Đặc biệt quan tâm:

- Rò rỉ dữ liệu chéo tổ chức hoặc chéo dự án
- Bỏ qua kiểm tra quyền ở cấp đối tượng
- Nâng quyền qua API key hoặc lời mời
- Bỏ qua xác thực ở cổng tự phục vụ của chủ thể dữ liệu, hoặc dò tìm sự tồn tại của email/số điện thoại
- Can thiệp được vào nhật ký kiểm toán
- Lỗi khiến dữ liệu đã yêu cầu xóa vẫn khôi phục được
- SSRF qua webhook
- Ghi được consent record giả, hoặc submission không có consent record

### Ghi chú về khôi phục tài khoản

Đặt lại mật khẩu qua email **không** bỏ qua xác thực hai lớp: tài khoản đã bật MFA vẫn phải nhập mã từ ứng dụng hoặc một mã dự phòng để hoàn tất. Nếu không, ai chiếm được hộp thư sẽ chiếm được tài khoản, và lớp thứ hai trở nên vô nghĩa.

Hệ quả: mất điện thoại **và** mất mã dự phòng thì tài khoản không khôi phục được qua giao diện. Đó là đánh đổi có chủ đích. Mã dự phòng được phát ngay lúc bật MFA, không phải để người dùng tự đi tìm sau — vì "sau" là lúc điện thoại đã mất rồi.

**Ngoài phạm vi** — lỗ hổng của bên thứ ba đã có CVE công khai (hãy báo cho họ), tấn công cần quyền truy cập vật lý vào máy chủ, social engineering, kết quả quét tự động không có bằng chứng khai thác, thiếu security header trên tài nguyên tĩnh không chứa dữ liệu.

## Phiên bản được hỗ trợ

Trong giai đoạn alpha (`0.x`), chỉ nhánh phát hành mới nhất nhận bản vá bảo mật.

## Ghi chú cho người tự vận hành

Collectr là công cụ. Bảo mật của hệ thống bạn chạy còn phụ thuộc vào:

- **`TENANT_KEK`** — bảo vệ như khóa gốc. Mất nó là mất vĩnh viễn dữ liệu nhạy cảm; lộ nó là mọi lớp mã hóa trở nên vô nghĩa. Sao lưu **tách khỏi** bản sao lưu cơ sở dữ liệu.
- **TLS** — bắt buộc cho mọi truy cập từ bên ngoài. Cấu hình Caddy mặc định đã tự động cấp chứng chỉ.
- **Cập nhật** — phần lớn sự cố đến từ CVE cũ, không phải lỗi zero-day. Theo dõi bản phát hành.
- **Sao lưu** — bản sao lưu chưa từng khôi phục thử không phải là bản sao lưu. Diễn tập hằng tháng.
- **Rà soát quyền** — kiểm tra định kỳ ai có `submission.export` và `submission.read_sensitive`. Rò rỉ nội bộ phổ biến hơn tấn công từ bên ngoài.

Danh sách đầy đủ trước khi vận hành thật: [docs/07-operations.md](docs/07-operations.md#75-checklist-trước-khi-go-live).

## Sự cố lộ dữ liệu

Nếu bạn tự vận hành Collectr và xảy ra sự cố lộ dữ liệu cá nhân, nghĩa vụ thông báo cho cơ quan chức năng và cho chủ thể dữ liệu thuộc về bạn với tư cách Bên Kiểm soát dữ liệu. Nhật ký kiểm toán của Collectr được thiết kế để phục vụ đúng việc điều tra này — nó ghi lại ai đã truy cập dữ liệu nào và vào lúc nào.
