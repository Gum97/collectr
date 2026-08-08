# 11. Phân quyền — Organization · Project · User

## 11.1 Cấu trúc phân cấp

```
Organization (= tenant, biên giới dữ liệu tuyệt đối)
 └── Project        (nhóm công việc: "Chiến dịch Tết 2026", "Tuyển dụng", "CSKH")
      ├── Forms
      ├── Links
      └── Webhooks / API keys
```

**Vì sao có Project chứ không chỉ Organization:** thực tế của khách hàng doanh nghiệp là phòng Marketing không được xem dữ liệu ứng viên của phòng Nhân sự. Nếu chỉ có một cấp, mọi thành viên thấy mọi dữ liệu cá nhân trong tổ chức — trái nguyên tắc tối thiểu hóa truy cập. Project là đơn vị phân quyền nhỏ nhất; **không phân quyền tới từng form** (độ phức tạp tăng vọt, giá trị thêm ít).

```sql
CREATE TABLE iam.projects (
  id UUID PRIMARY KEY, tenant_id UUID NOT NULL,
  name TEXT NOT NULL, slug TEXT NOT NULL,
  default_retention_days INT,               -- kế thừa xuống form nếu form không đặt riêng
  archived_at TIMESTAMPTZ,
  created_by UUID NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, slug)
);

CREATE TABLE iam.org_members (
  tenant_id UUID NOT NULL, user_id UUID NOT NULL,
  role TEXT NOT NULL,                       -- owner | admin | member | dpo
  joined_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, user_id)
);

CREATE TABLE iam.project_members (
  tenant_id UUID NOT NULL, project_id UUID NOT NULL, user_id UUID NOT NULL,
  role TEXT NOT NULL,                       -- manager | editor | analyst | viewer
  granted_by UUID NOT NULL, granted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (project_id, user_id)
);
```

Quyền hiệu lực = **hợp** của quyền cấp org và quyền cấp project. `member` cấp org không có quyền gì trên dữ liệu cho tới khi được thêm vào một project cụ thể — mặc định là **không thấy gì**, không phải "thấy tất cả rồi trừ dần".

## 11.2 Capability

Kiểm quyền theo capability, không theo tên vai trò. Vai trò chỉ là gói capability đặt sẵn.

Mười sáu capability sau **đang được thực thi trong code** (`internal/platform/authn/authn.go`):

```
member.manage
form.read  form.write  form.publish
link.read  link.write  link.delete
submission.read  submission.read_sensitive  submission.export
analytics.read
consent.manage        # sửa văn bản đồng ý, mục đích xử lý
dsr.handle            # xử lý yêu cầu của chủ thể dữ liệu
audit.read
apikey.manage  webhook.manage
```

`dsr.handle` cho phép nhân viên **sửa dữ liệu hộ chủ thể** — nhưng chỉ trong lúc
đáp ứng một yêu cầu chỉnh sửa đã ghi nhận, kèm cách xác minh người gọi. Không có
đường nào để nhân viên mở một bản ghi rồi sửa tuỳ ý; yêu cầu chính là cơ sở pháp
lý và cũng là dấu vết. Ô nhạy cảm vẫn nằm ngoài tầm: chúng niêm phong bằng khoá
của chủ thể và đường này không niêm phong lại được.

`member.manage` cũng là quyền canh màn **Cài đặt tổ chức** (`/api/v1/org`). Nó
cho đổi tên tổ chức và các thiết lập của tổ chức — **không** cho đụng tới cấu
hình triển khai. Điểm cuối lưu trữ, khoá bí mật, giới hạn và thời hạn thuộc về
người vận hành tiến trình và chỉ đặt được bằng biến môi trường.

Ranh giới đó là có chủ đích và đáng giữ: một phiên quản trị bị chiếm mà đổi được
điểm cuối lưu trữ là mọi tệp đính kèm về sau đi thẳng sang bucket của kẻ tấn
công, chỉ bằng một lần gửi form và không cần chạm tới máy chủ. Đổi lúc đang chạy
còn bỏ rơi mọi thứ đã ghi — `files.storage_key` trỏ vào bucket mà tiến trình
không còn cấu hình để với tới.

> [!WARNING]
> Bản trước của tài liệu này còn liệt kê `project.read`, `project.manage`,
> `form.delete`, `submission.edit`, `submission.delete`, `org.settings` và
> `org.billing`. **Chúng chưa tồn tại trong code.** Đó là thiết kế, không phải
> mô tả — và một tài liệu phân quyền mô tả những quyền không được kiểm là thứ
> nguy hiểm nhất trong cả tập tài liệu này: người đọc kết luận rằng một thao tác
> đang được canh gác, trong khi thứ canh nó là một capability khác hoặc không có
> gì cả.
>
> Các thao tác tương ứng hiện được canh bằng những capability khác — ví dụ xoá
> dự án và thu hồi vai trò dự án đi qua `member.manage`. Bảng dưới đã bỏ những
> dòng không thực thi được; đừng thêm lại cho tới khi code kiểm chúng.

Ba capability tách riêng có chủ đích:

| Capability | Vì sao tách |
|---|---|
| `submission.read_sensitive` | Đọc được bản ghi ≠ được nhìn dữ liệu nhạy cảm. Tách ra để một analyst làm việc bình thường mà không bao giờ chạm vào field nhạy cảm |
| `submission.export` | Xuất hàng loạt là hành vi rủi ro nhất trong hệ thống. Xem trên màn hình và tải cả kho về là hai việc khác nhau |
| `audit.read` | Người bị giám sát không nên tự đọc được nhật ký giám sát mình |

## 11.3 Vai trò cấp Organization

| Vai trò | Capability | Ghi chú |
|---|---|---|
| **owner** | tất cả 16 capability, và là vai trò duy nhất xoá được tổ chức | ≥ 1, không thể tự hạ cấp nếu là người cuối cùng |
| **admin** | như owner trừ xóa org / chuyển quyền sở hữu | |
| **member** | không có gì ở cấp org | Chỉ có quyền qua project membership |
| **dpo** | `audit.read` · `dsr.handle` · `consent.manage` · `submission.read` **trên mọi project** | Vai trò riêng cho người phụ trách bảo vệ dữ liệu. **Không có** `submission.export`, **không có** `form.write` — giám sát, không vận hành |

Vai trò `dpo` tồn tại vì nghĩa vụ tuân thủ cần một người nhìn xuyên mọi project, nhưng người đó không nên có quyền sửa dữ liệu hay tải dữ liệu về.

## 11.4 Vai trò cấp Project

| Capability | manager | editor | analyst | viewer |
|---|:---:|:---:|:---:|:---:|
| `form.read` | ✅ | ✅ | ✅ | ✅ |
| `form.write` | ✅ | ✅ | | |
| `form.publish` | ✅ | ✅ | | |
| `link.read` | ✅ | ✅ | ✅ | ✅ |
| `link.write` | ✅ | ✅ | | |
| `link.delete` | ✅ | | | |
| `submission.read` | ✅ | ✅ | ✅ | ✅ |
| `submission.read_sensitive` | ✅ | | | |
| `submission.export` | ✅ | | ✅ | |
| `analytics.read` | ✅ | ✅ | ✅ | ✅ |
| `webhook.manage` / `apikey.manage` | ✅ | | | |

`analyst` xuất được nhưng **không thấy field nhạy cảm** → file xuất ra tự động che `••••`. Đây là kết hợp hay gặp nhất trong thực tế: người làm báo cáo cần số liệu, không cần biết tình trạng sức khỏe của ai.

## 11.5 Thực thi — ba lớp

```
Lớp 1  Middleware      → xác thực, nạp capability của (user|api_key, project) từ Redis (TTL 60s)
Lớp 2  Handler         → RequireCap("submission.export") trên từng route
Lớp 3  Object-level    → mọi truy vấn mang project_id; kiểm bản ghi THUỘC project đã được cấp quyền
Lưới an toàn  RLS      → tenant_id ở tầng DB
```

> **Lớp 3 là lớp không được bỏ.** Kiểm "user có capability `submission.read`" mà không kiểm "bản ghi này thuộc project mà user có quyền" chính là lỗ hổng phân quyền cấp đối tượng — lớp lỗ hổng API phổ biến nhất. Không bao giờ tin `id` trong request.

Cache capability trong Redis TTL 60s, và **xóa key ngay khi thay đổi membership** — thu hồi quyền phải có hiệu lực tức thì, không đợi 60 giây.

## 11.6 Người dùng & mời tham gia

```sql
CREATE TABLE iam.users (
  id UUID PRIMARY KEY,
  email CITEXT UNIQUE NOT NULL,
  password_hash TEXT,                       -- argon2id; NULL nếu chỉ dùng OIDC
  name TEXT, avatar_url TEXT,
  mfa_secret_enc BYTEA, mfa_enabled BOOLEAN NOT NULL DEFAULT false,
  status TEXT NOT NULL DEFAULT 'active',    -- active | suspended | deleted
  last_login_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE iam.invitations (
  id UUID PRIMARY KEY, tenant_id UUID NOT NULL,
  email CITEXT NOT NULL, org_role TEXT NOT NULL,
  project_grants JSONB,                     -- [{project_id, role}]
  token_hash BYTEA NOT NULL,
  expires_at TIMESTAMPTZ NOT NULL,          -- 7 ngày
  accepted_at TIMESTAMPTZ, invited_by UUID NOT NULL
);
```

| Quy tắc | |
|---|---|
| Bắt buộc MFA cho `owner`, `admin`, `dpo` | Cấu hình được, mặc định bật |
| Rời tổ chức | Thu hồi mọi session + API key do người đó tạo, **trong vòng 60 giây** |
| Xóa user | Soft delete — `audit.entries` vẫn phải tham chiếu được tới người đã hành động |
| Ghi audit | Mọi thay đổi vai trò/thành viên, kể cả tự mình đổi |

## 11.7 Những gì cố tình **không** làm

| Không làm | Vì sao |
|---|---|
| Vai trò tùy biến (custom role builder) | 8 vai trò đặt sẵn phủ gần hết nhu cầu thực. Custom role kéo theo UI phức tạp và một lớp bug phân quyền mới |
| Phân quyền tới từng form | Project là đơn vị đủ nhỏ. Muốn cô lập hơn thì tạo project mới |
| Quyền phủ định (deny rule) | Kết hợp allow + deny sinh ra nghịch lý thứ tự ưu tiên. Chỉ dùng allow, hợp nhất bằng phép hợp |
| SSO/SCIM | Ngoài MVP. Cấu trúc `org_members` đã sẵn sàng để ánh xạ về sau |
