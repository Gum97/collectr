# 9. Báo cáo & Export Excel

Nguyên tắc: **export là hành vi truy cập hàng loạt dữ liệu cá nhân**, không phải một nút tải file. Mọi thiết kế dưới đây phục vụ hai mục tiêu cùng lúc — báo cáo có giá trị phân tích, và mỗi lần xuất đều để lại vết.

---

## 9.1 Cơ chế sinh file

```
POST /api/v1/forms/{id}/exports  {format, from, to, filters, include_sensitive}
  → 202 {job_id}                      (đồng bộ là sai: 50k dòng × 40 cột không thể trả trong 1 request)
  → worker sinh file, upload qua Storage
  → GET /api/v1/exports/{job_id} → {status, download_url, expires_at}
```

| Vấn đề | Cách xử lý |
|---|---|
| RAM khi 100k dòng | `excelize` **StreamWriter** — ghi từng dòng ra đĩa, không dựng cả workbook trong bộ nhớ |
| Giới hạn 1.048.576 dòng/sheet của XLSX | > 1M dòng → tự chia `Dữ liệu (1)`, `Dữ liệu (2)`…; > 5M → ép về CSV và báo cho người dùng |
| File chứa DLCN nằm lâu trên đĩa | `download_url` TTL **15 phút**, single-use; file bị xóa sau 24h bởi sweeper |
| Job treo | timeout 10 phút, `attempts ≤ 3`, thất bại → trạng thái `failed` + lý do |

```sql
CREATE TABLE core.exports (
  id UUID PRIMARY KEY, tenant_id UUID NOT NULL, project_id UUID NOT NULL,
  kind TEXT NOT NULL,                    -- form_report | link_report | dsr_package
  target_id UUID NOT NULL,
  params JSONB NOT NULL,                 -- from, to, filters, include_sensitive
  requested_by UUID NOT NULL,
  status TEXT NOT NULL DEFAULT 'queued', -- queued|running|ready|failed|expired
  row_count INT, storage_key TEXT, expires_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

## 9.2 Kiểm soát tuân thủ khi export

Áp dụng **trước** khi ghi dòng đầu tiên:

| Quy tắc | Hiện thực |
|---|---|
| Loại bỏ bản ghi đã xóa / hạn chế xử lý | `WHERE status = 'active'` |
| Loại chủ thể đã rút đồng ý cho mục đích của lần export này | tham số `purpose` bắt buộc khi `kind=marketing`; join `consent.current_consents` |
| Field `sensitive:true` **mặc định che** | hiện `••••` trừ khi người xuất có capability `submission.read_sensitive` **và** truyền `include_sensitive=true` |
| Ghi vết bắt buộc | `audit.write("submission.read_bulk", {form_id, row_count, filters, include_sensitive})` |
| Đánh dấu nguồn gốc | Sheet cuối ghi ai xuất, lúc nào, bộ lọc gì; footer mỗi sheet ghi `Xuất bởi <email> lúc <ts> — chứa dữ liệu cá nhân` |
| Rate limit | 10 export/giờ/user; > 3 lần export toàn bộ trong 1 giờ → cảnh báo cho org admin |

> Dòng cuối là chống rò rỉ nội bộ: kẻ sắp nghỉ việc tải sạch database khách hàng là kịch bản phổ biến hơn nhiều so với bị hack từ bên ngoài.

---

## 9.3 Báo cáo Form — cấu trúc workbook

### Sheet 1 · Tổng quan

| Chỉ số | Ý nghĩa |
|---|---|
| Tổng lượt xem form / lượt bắt đầu điền / lượt gửi | ba mốc funnel |
| **Tỉ lệ hoàn thành** = submit / form_view | chỉ số sức khỏe chính của form |
| Tỉ lệ bỏ giữa chừng = 1 − submit/form_start | tách riêng "không thèm bắt đầu" và "bắt đầu rồi bỏ" — hai vấn đề khác nhau |
| **Thời gian điền: trung vị + p90** | dùng trung vị, không dùng trung bình: một người mở tab 3 tiếng làm hỏng số trung bình |
| Biểu đồ theo ngày | lượt gửi/ngày, có đánh dấu mốc publish version mới |
| Phân bổ thiết bị / trình duyệt / khu vực | từ `meta` (ua_family, country) |
| Top 5 link nguồn | link nào mang về nhiều submission nhất |
| **Tỉ lệ đồng ý theo từng mục đích** | ví dụ marketing 43% — con số quyết định giá trị thật của danh sách thu được |
| Số bản ghi bị loại khỏi báo cáo | do đã xóa / hạn chế / rút đồng ý — **luôn hiển thị, không im lặng bỏ qua** |

### Sheet 2 · Dữ liệu (submission grid)

Cột cố định: `#` · `submission_id` · `_submitted_at` · `_form_version` · `_source_link` · `_device` · `_country` · `_consents` (chuỗi `service:yes;marketing:no`) · `_status`

Cột động: **column registry hợp nhất mọi version** ([6.2](06-deep-dives.md#grid-nhiều-version--column-registry)), giữ đúng ngữ nghĩa ba trạng thái ô:

| Ô | Nghĩa |
|---|---|
| `—` | không hỏi ở version này |
| `∅` | ẩn theo nhánh rẽ |
| ô trống | có hỏi, người dùng bỏ trống |

Cột của field đã gỡ được tô xám + tiêu đề ghi `(gỡ từ v4)`. Field đổi type tách thành hai cột riêng.

Bật `AutoFilter` + freeze dòng tiêu đề — chi tiết nhỏ nhưng là thứ đầu tiên người dùng làm bằng tay nếu ta không làm sẵn.

### Sheet 3 · Phân tích theo câu hỏi

Mỗi câu hỏi một khối, thống kê **theo đúng kiểu field**:

| Kiểu | Chỉ số |
|---|---|
| `choice` / `dropdown` | phân bố lựa chọn: số lượt + % (mẫu số = số người **thực sự thấy** câu này, không phải tổng submission) |
| `multi_choice` | số lượt/% mỗi option + **trung bình số option được chọn** + cặp option hay đi cùng nhau |
| `rating` | trung bình, trung vị, phân bố 1–5, **% điểm ≤ 2** (nhóm cần chăm sóc) |
| `text` | tỉ lệ trả lời, độ dài trung vị, 20 câu trả lời gần nhất |
| `date` | min/max, phân bố theo tháng |
| `file` | tỉ lệ đính kèm, dung lượng trung bình, phân bố loại file |

> **Mẫu số phải là số người thấy câu hỏi, không phải tổng submission.** Với form có rẽ nhánh, lấy tổng submission làm mẫu số sẽ khiến mọi câu hỏi nằm sau nhánh trông như có tỉ lệ trả lời thảm hại. Đây là lý do `visible_fields` được lưu trong DB ([4.6](04-data-model.md#46-submissions)) — thiếu nó thì sheet này không thể đúng.

### Sheet 4 · Rơi rớt theo trang

| Trang | Vào | Ra | Tỉ lệ rớt | Thời gian ở lại (trung vị) |
|---|---|---|---|---|

Cho biết **chính xác trang nào làm người dùng bỏ cuộc** — thông tin có giá trị hành động cao nhất trong cả báo cáo.

Cần thêm một loại event: `form_page_view {page_id}`. Chi phí: +1 event/trang/lượt điền ≈ +120k event/ngày (gấp ~1,35 lần tải hiện tại, vẫn nằm gọn trong ước lượng [bước 2](02-estimation.md)). Đáng đổi.

### Sheet 5 · Đồng ý

| Mục đích | Số đồng ý | % | Số rút | Rút sau bao lâu (trung vị) | Version văn bản |
|---|---|---|---|---|---|

Cột "rút sau bao lâu" là tín hiệu sớm: rút hàng loạt trong 24h đầu thường nghĩa là văn bản đồng ý đang gây hiểu nhầm hoặc chiến dịch gửi tin quá dày.

### Sheet 6 · Thông tin xuất

Ai xuất · lúc nào · bộ lọc gì · có bao gồm field nhạy cảm không · số dòng · số dòng bị loại và lý do · danh sách version schema có trong file kèm mô tả field từng version.

Sheet này là bản sao đọc được của một dòng audit log. Sáu tháng sau, người cầm file sẽ cần biết nó được lọc như thế nào.

---

## 9.4 Báo cáo Link — cấu trúc workbook

### Sheet 1 · Tổng quan
Tổng click · lượt truy cập duy nhất · số link hoạt động/hết hạn · biểu đồ click theo ngày · tỉ lệ 404/410 (link chết đang được phát tán).

### Sheet 2 · Theo từng link

| Cột | Ghi chú |
|---|---|
| code · alias · URL đích / form gắn kèm | |
| Tổng click · lượt duy nhất · **tỉ lệ lặp** | tỉ lệ lặp cao = QR dán tại chỗ, người ta quét nhiều lần |
| Click đầu tiên / gần nhất | link chưa từng được click là tín hiệu chiến dịch chưa chạy |
| **→ form_view · → submit · tỉ lệ chuyển đổi** | chỉ có với link gắn form — đây là cột đáng giá nhất |
| Trạng thái · hết hạn lúc | |

### Sheet 3 · Theo thời gian
Click theo **giờ trong ngày** × **ngày trong tuần** (bảng nhiệt 24×7). Trả lời trực tiếp câu hỏi "nên gửi chiến dịch lúc mấy giờ".

### Sheet 4 · Nguồn & thiết bị
Referrer domain · `utm_source/medium/campaign` (parse từ URL đích) · thiết bị · trình duyệt · khu vực.

### Sheet 5 · QR so với click trực tiếp

QR sinh ra luôn nhúng `?src=qr` vào URL → phân biệt được lượt **quét** và lượt **bấm**. Hai kênh này có hành vi khác hẳn nhau (quét = đang đứng tại điểm bán, tỉ lệ chuyển đổi thường cao hơn nhiều) và trộn chung sẽ che mất điều đó.

| Kênh | Click | → submit | Tỉ lệ chuyển đổi |
|---|---|---|---|
| QR | | | |
| Trực tiếp | | | |

---

## 9.5 Định dạng khác

| Định dạng | Dùng khi | Ghi chú |
|---|---|---|
| **XLSX** | mặc định cho báo cáo | nhiều sheet, có định dạng, `AutoFilter`, freeze pane |
| **CSV** | > 1M dòng, hoặc nạp vào hệ thống khác | chỉ sheet Dữ liệu; UTF-8 **có BOM** để Excel bản Việt không vỡ tiếng Việt |
| **JSON** | tích hợp qua API | dùng endpoint API ở [doc 10](10-public-api.md), không qua export job |
| **PDF** | gói trả lời yêu cầu truy cập của chủ thể dữ liệu | bản in được của submission + toàn bộ lịch sử đồng ý |

CSV có BOM là chi tiết vụn nhưng bỏ qua thì mọi người dùng Việt Nam mở file lần đầu đều thấy `Nguy?n V?n A`.

## Báo cáo link (`link_report`)

`POST /api/v1/projects/{id}/link-exports` → workbook 4 sheet.

| Sheet | Nội dung |
|---|---|
| Tổng quan | kỳ báo cáo, tổng lượt bấm, số link có lưu lượng, lượt bấm theo ngày |
| Link | mỗi link một dòng: mã, URL, đích, lượt bấm, lượt gửi, tỉ lệ chuyển đổi, lượt quét QR, dải mạng |
| Nguồn & chiến dịch | QR/link, trang dẫn nguồn, `utm_source` / `utm_medium` / `utm_campaign` |
| Nguồn gốc | ai xuất, lúc nào, bộ lọc, **và cách đọc từng con số** |

Quyền: **`analytics.read`**, không phải `submission.export`. Workbook chứa số lượt bấm và nhãn chiến dịch, không chứa câu trả lời của ai. Bắt buộc quyền xuất dữ liệu sẽ có nghĩa là không ai xem được link của mình chạy ra sao mà không đồng thời được phép trích xuất dữ liệu cá nhân.

Vẫn ghi audit: nó không chứa câu trả lời, nhưng vẫn là một lần trích xuất hàng loạt kết quả chiến dịch, và ai kéo nó ra là điều đáng biết.

**Cột "Dải mạng" không phải số người**, và sheet Nguồn gốc nói thẳng điều đó. Đây là loại cột mà người đọc sẽ mặc định hiểu là số khách truy cập rồi ra quyết định dựa trên nó.

Hai cột QR và Dải mạng lấy từ sự kiện thô nên chỉ phủ trong hạn `RAW_EVENT_RETENTION_DAYS`, còn cột Lượt bấm lấy từ rollup và phủ toàn bộ kỳ. Câu lưu ý này lặp lại ở **cả ba sheet** có số liệu — người mở sheet thứ ba chưa từng nhìn thấy sheet thứ nhất.

### Ô "Người xuất" từng rỗng

Trên mọi lần xuất, kể cả báo cáo biểu mẫu. `RequestInput.ActorEmail` tồn tại nhưng không handler nào điền, và `authn.Actor` không mang email. Giờ tra từ `RequestedBy` — id người dùng đã được ghi audit — chứ không tin vào trường do client gửi lên: sheet nguồn gốc sinh ra để nói ai đã kéo dữ liệu, mà một trường do người gọi tự điền thì không chứng minh được gì.
