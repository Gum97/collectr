package reporting

import (
	"fmt"
	"io"
	"time"

	"github.com/xuri/excelize/v2"

	"github.com/collectr/collectr/internal/contracts"
)

// WriteLinkReport renders a project's link report as a workbook.
//
// A separate writer from the submission report rather than a mode of it: that
// one is built around a form's answer columns and streams tens of thousands of
// rows, while this one has a fixed shape and as many rows as the project has
// links. Sharing the code would mean a column model that fits neither.
func WriteLinkReport(out io.Writer, r contracts.LinkReport, meta WorkbookMeta) error {
	f := excelize.NewFile()
	defer func() { _ = f.Close() }()
	_ = f.SetSheetName("Sheet1", "Tổng quan")

	clicks, submits := r.Totals()

	// The raw window is stated wherever a raw-derived number appears, not once at
	// the front. A reader opening the third sheet did not see the front.
	rawNote := fmt.Sprintf("cột QR và Dải mạng chỉ tính từ %s",
		r.BreakdownFrom.Format(time.DateOnly))

	summary := [][]any{
		{"Dự án", r.ProjectName},
		{"Kỳ báo cáo", period(r.From, r.To)},
		{},
		{"Tổng lượt bấm", clicks},
		{"Số link có lưu lượng", countActive(r.Rows)},
		{"Tổng số link", len(r.Rows)},
	}
	// Only shown when a form sits behind at least one link. For a pure shortener
	// a conversion rate is a ratio against something that cannot happen, and
	// printing 0% would read as a failure rather than as "not applicable".
	if submits > 0 {
		summary = append(summary,
			[]any{"Lượt gửi biểu mẫu", submits},
			[]any{"Tỉ lệ chuyển đổi", pct(float64(submits) / float64(clicks))})
	}
	summary = append(summary, []any{}, []any{"Lưu ý", rawNote})

	if len(r.ByDay) > 0 {
		summary = append(summary, []any{}, []any{"Lượt bấm theo ngày"})
		for _, p := range r.ByDay {
			summary = append(summary, []any{p.Bucket.Format(time.DateOnly), p.Clicks})
		}
	}
	if err := writeRows(f, "Tổng quan", summary); err != nil {
		return err
	}

	links := [][]any{{
		"Mã", "URL rút gọn", "Đích", "Trạng thái", "Lượt bấm",
		"Lượt gửi", "Tỉ lệ chuyển đổi", "Lượt quét QR", "Dải mạng",
		"Tạo lúc", "Lượt bấm gần nhất",
	}}
	for _, row := range r.Rows {
		conv := any("—")
		if row.Submits > 0 {
			conv = pct(row.ConversionRate())
		}
		links = append(links, []any{
			row.Code,
			"https://" + row.Host + "/r/" + row.Code,
			targetOf(row),
			row.Status,
			row.Clicks,
			row.Submits,
			conv,
			row.QRClicks,
			row.Networks,
			row.CreatedAt.Format(time.DateOnly),
			formatOptionalTime(row.LastClick),
		})
	}
	if err := writeSheetOn(f, "Link", links); err != nil {
		return err
	}
	if len(r.Rows) > 0 {
		last, err := excelize.CoordinatesToCellName(len(links[0]), len(links))
		if err != nil {
			return fmt.Errorf("resolving filter range: %w", err)
		}
		if err := f.AutoFilter("Link", "A1:"+last, nil); err != nil {
			return fmt.Errorf("setting autofilter: %w", err)
		}
	}

	campaign := [][]any{{"Lưu ý", rawNote}, {}}
	for _, section := range []struct {
		title string
		rows  []contracts.Breakdown
	}{
		{"Nguồn (QR hay link)", r.Sources},
		{"Trang dẫn nguồn", r.Referrers},
		{"utm_source", r.UTMSources},
		{"utm_medium", r.UTMMediums},
		{"utm_campaign", r.UTMCampaigns},
	} {
		if len(section.rows) == 0 {
			continue
		}
		campaign = append(campaign, []any{section.title, "Lượt bấm", "Dải mạng"})
		for _, b := range section.rows {
			campaign = append(campaign, []any{b.Key, b.Clicks, b.Networks})
		}
		campaign = append(campaign, []any{})
	}
	if err := writeSheetOn(f, "Nguồn & chiến dịch", campaign); err != nil {
		return err
	}

	provenance := [][]any{
		{"Người yêu cầu", meta.RequestedBy},
		{"Thời điểm", meta.RequestedAt.Format(time.RFC3339)},
		{"Bộ lọc", meta.Filters},
		{},
		{"Cách đọc các con số", ""},
		{"Lượt bấm", "gộp từ bảng rollup, phủ toàn bộ kỳ báo cáo"},
		{"Lượt quét QR, Dải mạng", rawNote},
		// Spelled out because "networks" is the kind of column a reader will
		// otherwise take for a visitor count and build a decision on.
		{"Dải mạng", "số dải /24 khác nhau, KHÔNG phải số người. " +
			"Hệ thống không nhận diện người truy cập giữa các lần."},
	}
	if err := writeSheetOn(f, "Nguồn gốc", provenance); err != nil {
		return err
	}

	if err := f.Write(out); err != nil {
		return fmt.Errorf("writing workbook: %w", err)
	}
	return nil
}

func countActive(rows []contracts.LinkReportRow) int {
	n := 0
	for _, r := range rows {
		if r.Clicks > 0 {
			n++
		}
	}
	return n
}

func targetOf(r contracts.LinkReportRow) string {
	if r.Target != "" {
		return r.Target
	}
	return "(biểu mẫu)"
}

func formatOptionalTime(t *time.Time) any {
	if t == nil {
		return "—"
	}
	return t.Format(time.DateOnly)
}

func writeSheetOn(f *excelize.File, name string, rows [][]any) error {
	if _, err := f.NewSheet(name); err != nil {
		return fmt.Errorf("creating sheet %q: %w", name, err)
	}
	return writeRows(f, name, rows)
}

func writeRows(f *excelize.File, name string, rows [][]any) error {
	for i, row := range rows {
		cell, err := excelize.CoordinatesToCellName(1, i+1)
		if err != nil {
			return fmt.Errorf("resolving cell on %q: %w", name, err)
		}
		if err := f.SetSheetRow(name, cell, &row); err != nil {
			return fmt.Errorf("writing row on %q: %w", name, err)
		}
	}
	return nil
}
