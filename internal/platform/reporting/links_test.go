package reporting

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"

	"github.com/collectr/collectr/internal/contracts"
)

func sampleLinkReport() contracts.LinkReport {
	last := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)
	return contracts.LinkReport{
		ProjectName:   "Tết 2026",
		From:          time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		To:            time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC),
		BreakdownFrom: time.Date(2026, 5, 9, 0, 0, 0, 0, time.UTC),
		Rows: []contracts.LinkReportRow{
			{Code: "tet26", Host: "rutgon.vn", Target: "https://acme.vn/tet",
				Status: "active", Clicks: 200, QRClicks: 50, Networks: 4, LastClick: &last},
			{Code: "form1", Host: "rutgon.vn", Status: "active",
				Clicks: 100, Submits: 25, Networks: 30},
		},
		ByDay:        []contracts.ClickPoint{{Bucket: last, Clicks: 300}},
		Sources:      []contracts.Breakdown{{Key: "qr", Clicks: 50, Networks: 4}},
		UTMCampaigns: []contracts.Breakdown{{Key: "tet2026", Clicks: 120, Networks: 20}},
	}
}

func TestWriteLinkReport(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	err := WriteLinkReport(&buf, sampleLinkReport(), WorkbookMeta{
		RequestedBy: "chu@quan.vn",
		RequestedAt: time.Date(2026, 8, 7, 4, 0, 0, 0, time.UTC),
		Filters:     "2026-08-01 → 2026-08-07",
	})
	if err != nil {
		t.Fatalf("WriteLinkReport: %v", err)
	}

	f, err := excelize.OpenReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("opening workbook: %v", err)
	}
	defer f.Close()

	for _, want := range []string{"Tổng quan", "Link", "Nguồn & chiến dịch", "Nguồn gốc"} {
		if idx, _ := f.GetSheetIndex(want); idx < 0 {
			t.Errorf("sheet %q missing, got %v", want, f.GetSheetList())
		}
	}

	rows, err := f.GetRows("Link")
	if err != nil {
		t.Fatalf("reading link sheet: %v", err)
	}
	// Header plus one row per link.
	if len(rows) != 3 {
		t.Fatalf("Link sheet has %d rows, want 3", len(rows))
	}
	if rows[1][0] != "tet26" {
		t.Errorf("first link code = %q, want tet26", rows[1][0])
	}
	if rows[1][1] != "https://rutgon.vn/r/tet26" {
		t.Errorf("short url = %q", rows[1][1])
	}
	// A link with no form behind it gets a dash, not 0%: a conversion rate
	// against something that cannot happen reads as a failure.
	if rows[1][6] != "—" {
		t.Errorf("conversion for a plain link = %q, want an em dash", rows[1][6])
	}
	if rows[2][6] != "25.0%" {
		t.Errorf("conversion for a form link = %q, want 25.0%%", rows[2][6])
	}
	if rows[2][2] != "(biểu mẫu)" {
		t.Errorf("target for a form link = %q", rows[2][2])
	}
}

// TestLinkReportStatesTheRawWindow: the QR and network columns cover a shorter
// period than the click column beside them. Every sheet showing one must say so,
// because a reader who opens only the third sheet never saw the first.
func TestLinkReportStatesTheRawWindow(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	if err := WriteLinkReport(&buf, sampleLinkReport(), WorkbookMeta{}); err != nil {
		t.Fatalf("WriteLinkReport: %v", err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("opening workbook: %v", err)
	}
	defer f.Close()

	for _, sheet := range []string{"Tổng quan", "Nguồn & chiến dịch", "Nguồn gốc"} {
		rows, err := f.GetRows(sheet)
		if err != nil {
			t.Fatalf("reading %s: %v", sheet, err)
		}
		var text strings.Builder
		for _, r := range rows {
			text.WriteString(strings.Join(r, " "))
		}
		if !strings.Contains(text.String(), "2026-05-09") {
			t.Errorf("sheet %q does not state the raw-event window", sheet)
		}
	}

	// And the network column must never be labelled as people.
	rows, _ := f.GetRows("Nguồn gốc")
	var text strings.Builder
	for _, r := range rows {
		text.WriteString(strings.Join(r, " "))
	}
	if !strings.Contains(text.String(), "KHÔNG phải số người") {
		t.Error("provenance sheet does not warn that networks are not people")
	}
}

func TestPeriodHandlesZeroBounds(t *testing.T) {
	t.Parallel()

	day := time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		from, to  time.Time
		wantNoYr1 bool
		want      string
	}{
		{name: "both set", from: day.AddDate(0, 0, -6), to: day, want: "2026-08-01 → 2026-08-07"},
		{name: "both zero", want: "toàn bộ"},
		// The case seen in a real export: a zero From printed as 0001-01-01,
		// which looks like a date and gets read as one.
		{name: "from zero", to: day, want: "toàn bộ dữ liệu đến 2026-08-07"},
		{name: "to zero", from: day, want: "từ 2026-08-07"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := period(tc.from, tc.to)
			if got != tc.want {
				t.Errorf("period() = %q, want %q", got, tc.want)
			}
			if strings.Contains(got, "0001-01-01") {
				t.Errorf("period() = %q, leaks the zero time", got)
			}
		})
	}
}
