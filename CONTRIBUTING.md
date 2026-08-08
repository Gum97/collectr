# Đóng góp cho Collectr

Cảm ơn bạn đã quan tâm. Tài liệu này ngắn gọn ở phần thông thường và khắt khe ở phần chạm vào dữ liệu cá nhân.

## Bắt đầu

```bash
git clone https://github.com/<org>/collectr.git
cd collectr
cp .env.example .env
make secrets
make dev            # postgres + redis qua docker, app chạy với hot reload
make test           # unit + integration (dùng testcontainers)
make lint           # golangci-lint + kiểm tra biên giới module
```

Yêu cầu: Go 1.24+, Docker, `make`.

## Quy trình

1. Mở issue trước khi làm tính năng lớn — để tránh bạn viết xong rồi mới biết hướng đi khác.
2. Nhánh từ `main`, đặt tên `feat/…`, `fix/…`, `docs/…`.
3. Commit theo [Conventional Commits](https://www.conventionalcommits.org).
4. PR cần: mô tả *vì sao*, test cho hành vi mới, `make lint test` xanh.
5. Thay đổi hành vi → cập nhật tài liệu trong `docs/` trong cùng PR.

## Chuẩn mã nguồn

- Go idiomatic: theo Effective Go và Google Go Style Guide. `gofmt` bắt buộc.
- Lỗi được bọc kèm ngữ cảnh (`fmt.Errorf("...: %w", err)`), không nuốt lỗi im lặng.
- SQL chỉ dùng tham số hóa. Không nối chuỗi truy vấn, không ngoại lệ.
- **Không log dữ liệu cá nhân.** Không log `answers`, `evidence`, email/số điện thoại đầy đủ, token, hay khóa. Ghi `data_subject_id` là đủ để lần vết.
- Mọi truy vấn phải mang `tenant_id`. RLS là lưới an toàn, không phải lý do để lười.

### Biên giới module

CI chạy `make lint-arch` để kiểm tra:

- `internal/modules/X/store` chỉ được import bởi `internal/modules/X/**`
- Module chỉ import `internal/contracts` của module khác, không bao giờ import package nội bộ
- `consent` và `audit` **không import bất kỳ module nghiệp vụ nào**
- Không JOIN xuyên schema PostgreSQL
- `internal/platform` không import `internal/modules`

Vi phạm sẽ làm hỏng build. Nếu bạn thấy luật này cản đường, hãy mở issue thay vì tìm cách lách — thường đó là dấu hiệu giao diện trong `contracts` còn thiếu.

## Thay đổi nhạy cảm — cần rà soát kỹ hơn

Những vùng sau ảnh hưởng trực tiếp tới nghĩa vụ pháp lý của người vận hành. PR chạm vào chúng cần **hai người duyệt** và mô tả rõ tác động:

| Vùng | Vì sao |
|---|---|
| `internal/modules/consent` | Sai ở đây nghĩa là bằng chứng đồng ý không còn giá trị |
| `internal/modules/audit` | Mọi thay đổi cấu trúc đều có nguy cơ làm gãy chuỗi hash |
| `internal/platform/crypto` | Xử lý DEK/KEK. Lỗi ở đây có thể khiến dữ liệu không giải mã được vĩnh viễn |
| Migration đụng schema `consent` / `audit` | Không thể sửa sai sau khi đã chạy trên dữ liệu thật |
| Bất kỳ đường nào ghi submission | Bất biến "không tồn tại submission thiếu consent record" phải được giữ |

Ba quy tắc **không được vi phạm trong bất kỳ hoàn cảnh nào**:

1. **Version biểu mẫu đã publish là bất biến.** Không sửa, không migrate.
2. **`consent.records` chỉ được thêm.** Rút đồng ý là thêm dòng mới, không phải cập nhật dòng cũ.
3. **`audit.entries` không bao giờ được UPDATE hay DELETE**, kể cả bởi migration.

## Kiểm thử

- Logic thuần (rule engine, validator schema, sinh code) → unit test + fuzz test.
- Mọi thứ chạm DB → integration test với testcontainers, không mock.
- Rule engine ở server và ở client dùng **chung bộ golden fixture** (`testdata/rules/`). Thêm rule mới thì thêm fixture — đây là thứ duy nhất giữ hai bên không lệch nhau.
- Sửa lỗi thì bắt đầu bằng một test tái hiện được lỗi đó.

## Tài liệu

`docs/` là nguồn sự thật về thiết kế. Nếu PR của bạn thay đổi một quyết định đã ghi trong [docs/08-decisions.md](docs/08-decisions.md), hãy cập nhật dòng đó kèm lý do — bảng ấy có giá trị chính ở chỗ nó ghi lại cả phương án bị loại.

## Giấy phép

Đóng góp của bạn được phát hành dưới [AGPL-3.0](LICENSE), cùng giấy phép với dự án.
