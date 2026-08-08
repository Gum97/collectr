# Giao diện Collectr

Hai bề mặt, hai cách dựng, có chủ đích.

| | Stack | Bundle (gzip) | Vì sao |
|---|---|---|---|
| `src/public/` | TypeScript thuần, không framework | **2,6 KB** | Trang khách hàng thật đứng đợi |
| `src/app/` | React 19 + Vite + TanStack Query | 183 KB | 24 màn sau đăng nhập, kích thước không phải chỉ số ai đo |

Chênh 70 lần, và đó là lý do có hai thư mục thay vì một.

Trang điền form là **mẫu số của chính tỉ lệ hoàn thành** mà sản phẩm này báo cáo. Bắt người trả lời tải runtime của trình dựng biểu mẫu để điền sáu ô là tự bắn vào con số đó rồi đi giải thích tại sao nó tụt.

## Chạy

```bash
npm install
npm run dev      # Vite ở :5173, proxy /api sang Go ở :8080
npm run test     # engine.ts chấm bằng fixture của Go
npm run build    # ghi thẳng vào ../internal/webui/dist để go:embed nhúng
```

`npm run build` rồi `go build ./cmd/collectr` là có một binary tự phục vụ giao diện. `docker compose up` làm sẵn cả hai bước — Node chỉ tồn tại ở tầng build, ảnh phát hành là distroless một binary.

Ở chế độ dev, Vite proxy `/api` sang Go nên **cookie phiên vẫn same-origin**. Tách origin là phải mở CORS, và CORS kèm cookie là phải nới `SameSite` — đúng thứ đang bảo vệ phiên quản trị. Nhiều đội chuyển sang token trong localStorage vì bước này, và đó là bước lùi.

## Bộ luật rẽ nhánh chạy hai lần

```
src/shared/engine.ts  ←→  internal/modules/forms/engine/engine.go
             ↘ cùng đọc ↙
        engine/testdata/golden.json
```

Client ẩn/hiện câu hỏi khi người ta trả lời. Server **tính lại toàn bộ** khi nhận bài gửi, vì client không đáng tin: giấu một field chỉ ngăn nó được hỏi, không ngăn nó được gửi lên.

Hai bản chấm bằng **cùng một tệp fixture, không sao chép**. Bản sao là một nhánh rẽ có độ trễ — hai tệp giống nhau cho tới khi ai đó sửa một bên. `docker build` chạy bộ test đó **trước khi biên dịch**, nên không thể tạo ra một ảnh mà trình duyệt và máy chủ bất đồng về câu hỏi nào bắt buộc.

29 ca dùng chung: 9 ca đi đường, 20 ca toán tử. Hai mươi ca sau từng chỉ chạy phía Go — đúng những chỗ hai bản dễ lệch nhất (`in`, `contains`, `between`, so sánh ngày, ép kiểu số-chuỗi) thì bản client chưa hề được chấm.

## Bố cục

```
src/
├── shared/engine.ts        luật rẽ nhánh, dùng chung, chấm bằng fixture Go
├── public/form.ts          trang điền form — không framework
└── app/
    ├── lib/                api client (cookie), phiên, dự án
    ├── components/         Shell + ui.tsx (primitive dùng chung)
    └── routes/             24 màn, nhóm theo module
```

`ui.tsx` là nơi 24 màn lấy `Table` · `StatusPill` · `Callout` · `pct()` · `deadline()`. Thay bộ token trong `tailwind.config.js` là đổi được toàn bộ giao diện — bản mockup hi-fi được áp bằng cách sửa ba tệp, 41 tệp còn lại quét cơ học.

## Bốn quy tắc không được phá

**1. Không tự suy ra luật phân quyền ở client.** API trả `capabilities` và `access`. Dùng chúng. Client tính lại rồi sẽ lệch với API đang thực thi nó — và bên lệch không phải bên chặn.

**2. `0` khác `chưa đo được`.** `pct()` trả `—` khi không có mẫu số. Dự án này từng in *"tỉ lệ hoàn thành 0.0%"* cho một biểu mẫu có 108.441 lượt gửi, suốt nhiều phiên bản, vì mẫu số luôn bằng 0.

**3. Không chia hai con số khác nguồn.** API trả `clicks` (rollup, toàn lịch sử) và `breakdown_clicks` (sự kiện thô, trong hạn lưu). Lần đầu chia nhầm, endpoint trả về `repeat_rate: -3.127`.

**4. `networks` không phải số người.** Hệ thống cố ý không nhận diện người truy cập giữa các lần — `visit_id` sinh mới mỗi lượt chuyển hướng. Gọi nó là "khách truy cập" là nói dối bằng một cái nhãn.

## Font

Tự host, có subset tiếng Việt. CDN sẽ gửi địa chỉ của mọi người điền form cho bên thứ ba — đúng thứ sản phẩm này sinh ra để tránh, và CSP trong `deploy/Caddyfile` chặn sẵn.

Trang công khai **không** dùng font mockup mà dùng font hệ thống: bộ tối thiểu tốn 128 KB cho một module 2,6 KB. Nó theo mockup về bảng màu và bố cục, không theo về chữ.
