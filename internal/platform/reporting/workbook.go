package reporting

import (
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/xuri/excelize/v2"

	"github.com/collectr/collectr/internal/contracts"
)

// xlsxRowLimit is the hard maximum a worksheet can hold.
const xlsxRowLimit = 1_048_576

// Masked is what a sensitive answer looks like to someone without the capability
// to see it.
const Masked = "••••••"

// Cell markers. Three different empties, spelled out so a reader of the
// spreadsheet can tell them apart without consulting documentation.
const (
	markNotAsked = "—"
	markHidden   = "∅"
)

// WorkbookMeta is the provenance recorded inside every export.
type WorkbookMeta struct {
	RequestedBy      string
	RequestedAt      time.Time
	IncludeSensitive bool
	Filters          string
}

// Writer builds the report workbook.
//
// Rows are written through excelize's StreamWriter, which flushes to disk as it
// goes. Building the sheet in memory would put a year of responses in the heap
// at once, on the same process that is serving redirects.
type Writer struct {
	file    *excelize.File
	data    *excelize.StreamWriter
	columns []contracts.ExportColumn

	rowNum   int
	written  int
	overflow bool
}

// NewWriter starts a workbook for the given column set.
func NewWriter(columns []contracts.ExportColumn) (*Writer, error) {
	f := excelize.NewFile()

	// Sheet order follows how a person reads a report: the summary first, the
	// raw data second, the analysis after.
	if _, err := f.NewSheet("Dữ liệu"); err != nil {
		return nil, fmt.Errorf("creating data sheet: %w", err)
	}
	f.SetSheetName("Sheet1", "Tổng quan")

	sw, err := f.NewStreamWriter("Dữ liệu")
	if err != nil {
		return nil, fmt.Errorf("creating stream writer: %w", err)
	}

	// Before the first row: a StreamWriter refuses pane settings once writing has
	// begun. Freezing the header is worth doing because it is the first thing
	// anyone does by hand otherwise.
	if err := sw.SetPanes(&excelize.Panes{
		Freeze: true, XSplit: 0, YSplit: 1,
		TopLeftCell: "A2", ActivePane: "bottomLeft",
	}); err != nil {
		return nil, fmt.Errorf("freezing header: %w", err)
	}

	w := &Writer{file: f, data: sw, columns: columns, rowNum: 1}
	if err := w.writeDataHeader(); err != nil {
		return nil, err
	}
	return w, nil
}

// writeDataHeader lays out the fixed columns followed by the merged question set.
func (w *Writer) writeDataHeader() error {
	header := []any{"#", "Mã bản ghi", "Thời điểm gửi", "Phiên bản biểu mẫu",
		"Nguồn", "Thiết bị", "Khu vực", "Đồng ý", "Trạng thái"}

	for _, c := range w.columns {
		label := c.Label
		if c.TypeVariant != "" {
			// A field whose type changed gets one column per type; the header
			// says which, so two columns with the same question are not a puzzle.
			label += " (" + c.TypeVariant + ")"
		}
		if c.RetiredAfter > 0 {
			label += fmt.Sprintf(" [gỡ sau v%d]", c.RetiredAfter)
		}
		header = append(header, label)
	}

	cell, err := excelize.CoordinatesToCellName(1, w.rowNum)
	if err != nil {
		return fmt.Errorf("resolving header cell: %w", err)
	}
	if err := w.data.SetRow(cell, header); err != nil {
		return fmt.Errorf("writing data header: %w", err)
	}
	w.rowNum++
	return nil
}

// WriteRow appends one submission.
func (w *Writer) WriteRow(row contracts.ExportRow, includeSensitive bool) error {
	if w.rowNum >= xlsxRowLimit {
		// Silently truncating would make a partial extract look complete. The
		// caller reports the overflow instead.
		w.overflow = true
		return nil
	}

	values := []any{
		w.written + 1,
		row.ID.String(),
		row.SubmittedAt.Format("2006-01-02 15:04:05"),
		row.VersionNo,
		row.SourceLink,
		row.Device,
		row.Country,
		formatConsents(row.Consents),
		row.Status,
	}

	for _, c := range w.columns {
		values = append(values, formatCell(c, row.Cells[c.FieldID], includeSensitive))
	}

	cell, err := excelize.CoordinatesToCellName(1, w.rowNum)
	if err != nil {
		return fmt.Errorf("resolving row cell: %w", err)
	}
	if err := w.data.SetRow(cell, values); err != nil {
		return fmt.Errorf("writing row %d: %w", w.written+1, err)
	}
	w.rowNum++
	w.written++
	return nil
}

// Rows reports how many submissions were written.
func (w *Writer) Rows() int { return w.written }

// Overflowed reports whether rows were dropped at the sheet limit.
func (w *Writer) Overflowed() bool { return w.overflow }

// Finish writes the remaining sheets and streams the workbook to out.
func (w *Writer) Finish(out io.Writer, r Report, meta WorkbookMeta) error {
	if err := w.data.Flush(); err != nil {
		return fmt.Errorf("flushing data sheet: %w", err)
	}
	if w.written > 0 {
		last, err := excelize.CoordinatesToCellName(len(w.columns)+9, w.written+1)
		if err != nil {
			return fmt.Errorf("resolving filter range: %w", err)
		}
		if err := w.file.AutoFilter("Dữ liệu", "A1:"+last, nil); err != nil {
			return fmt.Errorf("setting autofilter: %w", err)
		}
	}

	for _, write := range []func(Report, WorkbookMeta) error{
		w.writeSummary, w.writeQuestions, w.writeDropOff, w.writeConsent, w.writeProvenance,
	} {
		if err := write(r, meta); err != nil {
			return err
		}
	}

	if err := w.file.Write(out); err != nil {
		return fmt.Errorf("writing workbook: %w", err)
	}
	return w.file.Close()
}

func (w *Writer) writeSummary(r Report, _ WorkbookMeta) error {
	rows := [][]any{
		{"Biểu mẫu", r.FormTitle},
		{"Kỳ báo cáo", period(r.From, r.To)},
		{},
		{"Lượt xem biểu mẫu", r.Funnel.Views},
		{"Lượt bắt đầu điền", r.Funnel.Starts},
		{"Lượt gửi", r.Funnel.Submits},
		{"Tỉ lệ hoàn thành", pct(r.Funnel.CompletionRate())},
		// Two different failures, kept apart: people who never began, and people
		// who began and gave up. They call for different fixes.
		{"Tỉ lệ bỏ giữa chừng", pct(r.Funnel.AbandonRate())},
		{},
		{"Số bản ghi trong báo cáo", r.Submissions},
		{"Số bản ghi bị loại", r.Excluded},
	}
	if r.Excluded > 0 {
		rows = append(rows, []any{"", "đã xóa, bị hạn chế xử lý, hoặc chủ thể đã rút đồng ý"})
	}

	rows = append(rows, []any{}, []any{"Phiên bản có trong dữ liệu", formatInts(r.Versions)})

	if len(r.ByDay) > 0 {
		rows = append(rows, []any{}, []any{"Lượt gửi theo ngày"})
		for _, d := range r.ByDay {
			rows = append(rows, []any{d.Day.Format(time.DateOnly), d.Count})
		}
	}
	for _, section := range []struct {
		title  string
		counts []LabelCount
	}{
		{"Thiết bị", r.Devices}, {"Khu vực", r.Countries}, {"Nguồn", r.Sources},
	} {
		if len(section.counts) == 0 {
			continue
		}
		rows = append(rows, []any{}, []any{section.title})
		for _, c := range section.counts {
			rows = append(rows, []any{c.Label, c.Count})
		}
	}

	return w.writeSheet("Tổng quan", rows)
}

func (w *Writer) writeQuestions(r Report, _ WorkbookMeta) error {
	if _, err := w.file.NewSheet("Theo câu hỏi"); err != nil {
		return fmt.Errorf("creating question sheet: %w", err)
	}

	rows := [][]any{{
		"Câu hỏi", "Loại", "Được hiển thị", "Có trả lời", "Tỉ lệ trả lời",
		"Không hỏi ở phiên bản đó", "Bị ẩn theo nhánh", "Chi tiết",
	}}

	for _, q := range r.Questions {
		rows = append(rows, []any{
			q.Label, q.Type, q.Shown, q.Answered, pct(q.AnswerRate()),
			q.NotAsked, q.Hidden, detail(q),
		})
		for _, o := range q.Options {
			rows = append(rows, []any{"    " + o.Label, "", o.Count, "", pct(o.Share)})
		}
		if q.Histogram != nil {
			for score := 1; score <= 10; score++ {
				if c, ok := q.Histogram[score]; ok {
					rows = append(rows, []any{"    " + strconv.Itoa(score) + " điểm", "", c})
				}
			}
		}
	}

	// The denominator is stated in the sheet itself, because a reader who
	// assumes it is the submission count will misread every branching form.
	rows = append(rows, []any{}, []any{
		"Ghi chú: mọi tỉ lệ tính trên số người THỰC SỰ được hiển thị câu hỏi, " +
			"không phải trên tổng số bản ghi.",
	})

	return w.writeSheet("Theo câu hỏi", rows)
}

func (w *Writer) writeDropOff(r Report, _ WorkbookMeta) error {
	if len(r.DropOff) == 0 {
		return nil
	}
	if _, err := w.file.NewSheet("Rơi rớt theo trang"); err != nil {
		return fmt.Errorf("creating drop-off sheet: %w", err)
	}

	rows := [][]any{{"Trang", "Số người vào", "Số người dừng lại", "Tỉ lệ rớt"}}
	for _, d := range r.DropOff {
		rows = append(rows, []any{d.PageID, d.Entered, d.Left, pct(d.Rate())})
	}
	return w.writeSheet("Rơi rớt theo trang", rows)
}

func (w *Writer) writeConsent(r Report, _ WorkbookMeta) error {
	if len(r.Consents) == 0 {
		return nil
	}
	if _, err := w.file.NewSheet("Đồng ý"); err != nil {
		return fmt.Errorf("creating consent sheet: %w", err)
	}

	rows := [][]any{{"Mục đích", "Số đồng ý", "Tổng số", "Tỉ lệ"}}
	for _, c := range r.Consents {
		rows = append(rows, []any{c.Purpose, c.Granted, c.Total, pct(c.Share())})
	}
	return w.writeSheet("Đồng ý", rows)
}

// writeProvenance records how this file came to exist.
//
// Six months from now somebody will be holding this workbook and needing to know
// what was filtered out of it and whether the blanks are masked or genuinely
// empty. This sheet is the readable copy of the audit entry.
func (w *Writer) writeProvenance(r Report, meta WorkbookMeta) error {
	if _, err := w.file.NewSheet("Thông tin xuất"); err != nil {
		return fmt.Errorf("creating provenance sheet: %w", err)
	}

	sensitive := "Đã che (••••••)"
	if meta.IncludeSensitive {
		sensitive = "HIỂN THỊ ĐẦY ĐỦ"
	}

	rows := [][]any{
		{"Người xuất", meta.RequestedBy},
		{"Thời điểm xuất", meta.RequestedAt.Format(time.RFC3339)},
		{"Bộ lọc", meta.Filters},
		{"Trường nhạy cảm", sensitive},
		{"Số dòng", r.Submissions},
		{"Số dòng bị loại", r.Excluded},
		{},
		{"Ý nghĩa ô trống:"},
		{markNotAsked, "phiên bản của bản ghi này không có câu hỏi đó"},
		{markHidden, "câu hỏi bị ẩn theo nhánh rẽ, người trả lời không nhìn thấy"},
		{"(trống)", "có hiển thị, người trả lời bỏ qua"},
		{},
		{"TỆP NÀY CHỨA DỮ LIỆU CÁ NHÂN. Lưu trữ và chia sẻ theo chính sách của tổ chức."},
	}
	if w.overflow {
		rows = append(rows, []any{},
			[]any{"CẢNH BÁO", fmt.Sprintf("Vượt giới hạn %d dòng của Excel; báo cáo bị cắt bớt.", xlsxRowLimit)})
	}
	return w.writeSheet("Thông tin xuất", rows)
}

func (w *Writer) writeSheet(name string, rows [][]any) error {
	for i, row := range rows {
		cell, err := excelize.CoordinatesToCellName(1, i+1)
		if err != nil {
			return fmt.Errorf("resolving cell on %s: %w", name, err)
		}
		if err := w.file.SetSheetRow(name, cell, &row); err != nil {
			return fmt.Errorf("writing row on %s: %w", name, err)
		}
	}
	return w.file.SetColWidth(name, "A", "A", 32)
}

// formatCell renders one answer, or the marker explaining its absence.
func formatCell(col contracts.ExportColumn, cell contracts.ExportCell, includeSensitive bool) any {
	switch cell.State {
	case contracts.CellNotAsked, "":
		return markNotAsked
	case contracts.CellHidden:
		return markHidden
	case contracts.CellBlank:
		return ""
	}

	if col.Sensitive && !includeSensitive {
		return Masked
	}

	switch v := cell.Value.(type) {
	case nil:
		return ""
	case string:
		return labelFor(col, v)
	case []any:
		var out string
		for i, item := range v {
			if i > 0 {
				out += ", "
			}
			if s, ok := item.(string); ok {
				out += labelFor(col, s)
			} else {
				out += fmt.Sprint(item)
			}
		}
		return out
	case map[string]any:
		// A file answer: the id is what identifies it, and the filename is not
		// carried through the grid.
		if id, ok := v["file_id"].(string); ok {
			return "tệp:" + id
		}
		return fmt.Sprint(v)
	default:
		return v
	}
}

// labelFor turns a stored option id into its human label.
//
// Answers are stored by id precisely so relabelling never corrupts them; the
// translation to text belongs here, at the moment of display.
func labelFor(col contracts.ExportColumn, id string) string {
	for _, o := range col.Options {
		if o.ID == id {
			return o.Label
		}
	}
	return id
}

func formatConsents(c map[string]bool) string {
	if len(c) == 0 {
		return ""
	}
	var out string
	for purpose, granted := range c {
		if out != "" {
			out += "; "
		}
		state := "không"
		if granted {
			state = "có"
		}
		out += purpose + ":" + state
	}
	return out
}

func formatInts(v []int) string {
	var out string
	for i, n := range v {
		if i > 0 {
			out += ", "
		}
		out += "v" + strconv.Itoa(n)
	}
	return out
}

func period(from, to time.Time) string {
	switch {
	case from.IsZero() && to.IsZero():
		return "toàn bộ"
	// Formatting a zero time gives 0001-01-01, which looks like a date and is
	// read as one.
	case from.IsZero():
		return "toàn bộ dữ liệu đến " + to.Format(time.DateOnly)
	case to.IsZero():
		return "từ " + from.Format(time.DateOnly)
	}
	return from.Format(time.DateOnly) + " → " + to.Format(time.DateOnly)
}

func pct(v float64) string {
	return strconv.FormatFloat(v*100, 'f', 1, 64) + "%"
}

func detail(q QuestionStats) string {
	switch q.Type {
	case "rating":
		if q.Answered == 0 {
			return ""
		}
		return fmt.Sprintf("TB %.2f · trung vị %.1f · ≤2 điểm %s",
			q.Mean, q.Median, pct(q.LowShare))
	case "multi_choice":
		return fmt.Sprintf("trung bình %.2f lựa chọn", q.MeanSelections)
	case "text":
		return fmt.Sprintf("độ dài trung vị %d ký tự", q.MedianLength)
	case "date":
		if q.Earliest == "" {
			return ""
		}
		return q.Earliest + " → " + q.Latest
	case "file":
		return "tỉ lệ đính kèm " + pct(q.AttachedShare)
	default:
		return ""
	}
}
