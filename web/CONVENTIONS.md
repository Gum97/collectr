# Quy ước giao diện Collectr

Đọc hết trước khi viết dòng đầu tiên. Mọi màn phải trông như do một người làm.

## Ngôn ngữ

**Toàn bộ chuỗi hiển thị bằng tiếng Việt.** Thuật ngữ kỹ thuật giữ nguyên tiếng Anh
khi đó là tên thật của thứ đó (`webhook`, `API key`, `TOTP`, `sha256`, tên field).
Comment trong code viết bằng tiếng Anh, theo phần còn lại của repo.

## Nguồn sự thật

- Wireframe từng màn: `<scratchpad>/screens/<id>.txt` (văn bản) và `.html` (bản vẽ
  đầy đủ, có style). Bám sát bố cục, nhãn và con số mẫu trong đó.
- Route API: `docs/03-api.md`. Đừng đoán tên endpoint — nếu thiếu, **báo lại**,
  đừng bịa ra rồi gọi.
- Kiểu dữ liệu chung của biểu mẫu: `src/shared/engine.ts`.

## Stack

React 19 + TypeScript strict + Tailwind + TanStack Query + react-router v7.
Không thêm dependency mới. Nếu nghĩ là cần, báo lại thay vì tự cài.

## Bảng màu (đã có trong tailwind.config.js — dùng tên, đừng viết mã hex)

`canvas` nền · `surface` thẻ · `panel` sidebar · `ink` chữ/viền đậm ·
`muted` chữ phụ · `faint` nhãn cột · `line` viền nhạt · `accent` nhấn ·
`overdue` quá hạn · `duesoon` sắp đến hạn · `ok` bình thường

Ba màu trạng thái tách riêng có lý do: "quá hạn SLA", "sắp đến hạn" và "dữ liệu
nhạy cảm" là ba việc khác nhau, gộp vào một màu `danger` sẽ xoá mất khác biệt đó.

## Lớp dùng sẵn (`src/index.css`)

`.input` · `.btn` · `.btn-primary` · `.id-chip` (mã/metadata, font mono 10px)

## Primitive dùng chung (`src/app/components/ui.tsx`)

`PageHeader` `Card` `Table` `Th` `Td` `StatusPill` `Empty` `ErrorBanner`
`Loading` `Field` `Money`-kiểu số. **Dùng chúng, đừng dựng lại.**

## Chữ

- Tiêu đề màn: `text-[15px] font-bold`
- Nội dung: `text-[12px]` hoặc `text-[13px]`
- Nhãn cột bảng: `font-mono text-[9px] tracking-widest text-faint`
- Mã/ID: `.id-chip`

Cỡ chữ nhỏ là do wireframe vẽ vậy — đây là ứng dụng nội bộ dùng cả ngày, mật độ
thông tin quan trọng hơn sự thoáng đãng.

## Quy tắc không được phá

1. **Không tự suy ra luật phân quyền ở client.** API trả về `capabilities` và các
   trường như `access`. Dùng chúng. Client tính lại luật rồi sẽ lệch với API đang
   thực thi nó.
2. **Không bao giờ hiện dữ liệu nhạy cảm mà không có nhãn.** Field nhạy cảm phải
   thấy rõ là nhạy cảm ở mọi nơi nó xuất hiện — danh sách, bảng, xuất file.
3. **Số 0 và "không đo được" là hai thứ khác nhau.** Đừng hiện `0%` khi thật ra là
   chưa có mẫu số. Dùng `—`.
4. **Lỗi phải nói được phải làm gì.** "Có lỗi xảy ra" là vô dụng. Nếu API trả
   `fields`, hiện từng lỗi cạnh đúng ô gây ra nó.
5. **Không `localStorage` cho bất cứ thứ gì thuộc phiên.** Phiên là cookie
   httpOnly, cố ý như vậy.
6. **Trạng thái rỗng phải nói vì sao rỗng**, không chỉ "không có dữ liệu".

## Truy vấn dữ liệu

```ts
const q = useQuery({
  queryKey: ['forms', projectId],
  queryFn: async () => (await api.get<List<FormRow>>(`/api/v1/forms?project_id=${projectId}`)).data,
  enabled: Boolean(projectId),
})
```

Ghi dùng `useMutation` + `qc.invalidateQueries`. Không tự quản state server bằng
`useState`.

## Định dạng số và ngày

Số: `n.toLocaleString('vi-VN')`. Ngày: `new Date(s).toLocaleDateString('vi-VN')`.
Khoảng thời gian còn lại ("còn 41h", "quá hạn 6h") dùng helper trong `ui.tsx`.

## Trợ năng

- Mọi input có `<label>` gắn `htmlFor`.
- Bảng có `<th scope="col">`.
- Thông báo lỗi `role="alert"`, trạng thái `role="status"`.
- Không dùng màu làm tín hiệu duy nhất — kèm chữ hoặc ký hiệu.

## Bạn KHÔNG được sửa

`src/app/main.tsx` (định tuyến — người điều phối sẽ nối), `src/index.css`,
`tailwind.config.js`, `src/shared/engine.ts`, `src/app/lib/*`,
`src/app/components/{Shell,ui}.tsx`.

Cần một primitive mới hoặc một route mới? **Báo lại trong phần trả về**, đừng tự
sửa tệp dùng chung — nhiều agent đang chạy song song trên cùng cây thư mục.

## Trả về gì

Danh sách tệp đã tạo, các route cần nối (đường dẫn → component), API nào còn
thiếu, và những chỗ bạn phải tự quyết vì wireframe không nói rõ.
