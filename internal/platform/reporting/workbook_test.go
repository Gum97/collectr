package reporting

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/xuri/excelize/v2"

	"github.com/collectr/collectr/internal/contracts"
)

func exportColumns() []contracts.ExportColumn {
	return []contracts.ExportColumn{
		{FieldID: "f_name", Label: "Họ tên", Type: "text"},
		{FieldID: "f_used", Label: "Đã dùng?", Type: "choice", Options: []contracts.ExportOption{
			{ID: "o_yes", Label: "Rồi"}, {ID: "o_no", Label: "Chưa"},
		}},
		{FieldID: "f_health", Label: "Sức khoẻ", Type: "text", Sensitive: true},
	}
}

func buildWorkbook(t *testing.T, rows []contracts.ExportRow, includeSensitive bool) *excelize.File {
	t.Helper()

	cols := exportColumns()
	w, err := NewWriter(cols)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}

	acc := NewAccumulator(cols)
	for _, r := range rows {
		acc.Add(r)
		if err := w.WriteRow(r, includeSensitive); err != nil {
			t.Fatalf("WriteRow: %v", err)
		}
	}

	report := acc.Build("Khảo sát", time.Time{}, time.Time{},
		contracts.FunnelSummary{Views: 100, Starts: 60, Submits: len(rows)}, nil, 3)

	var buf bytes.Buffer
	if err := w.Finish(&buf, report, WorkbookMeta{
		RequestedBy: "a@acme.vn", RequestedAt: time.Now(), IncludeSensitive: includeSensitive,
	}); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	f, err := excelize.OpenReader(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("reopening workbook: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func sampleRows() []contracts.ExportRow {
	return []contracts.ExportRow{
		{
			ID: uuid.New(), VersionNo: 2, SubmittedAt: time.Now().UTC(), Status: "active",
			Device: "chrome", Country: "VN", Consents: map[string]bool{"service": true},
			Cells: map[string]contracts.ExportCell{
				"f_name":   {State: contracts.CellAnswered, Value: "Nguyễn Văn A"},
				"f_used":   {State: contracts.CellAnswered, Value: "o_yes"},
				"f_health": {State: contracts.CellAnswered, Value: "tiểu đường type 2"},
			},
		},
		{
			ID: uuid.New(), VersionNo: 1, SubmittedAt: time.Now().UTC(), Status: "active",
			Cells: map[string]contracts.ExportCell{
				"f_name":   {State: contracts.CellAnswered, Value: "Trần Thị B"},
				"f_used":   {State: contracts.CellBlank},
				"f_health": {State: contracts.CellNotAsked},
			},
		},
		{
			ID: uuid.New(), VersionNo: 2, SubmittedAt: time.Now().UTC(), Status: "active",
			Cells: map[string]contracts.ExportCell{
				"f_name":   {State: contracts.CellAnswered, Value: "Lê Văn C"},
				"f_used":   {State: contracts.CellAnswered, Value: "o_no"},
				"f_health": {State: contracts.CellHidden},
			},
		},
	}
}

// TestSensitiveIsMaskedByDefault is the control that matters most in an export:
// a file of personal data leaves the system, and the sensitive columns must be
// unreadable unless the requester was entitled to them.
func TestSensitiveIsMaskedByDefault(t *testing.T) {
	t.Parallel()

	f := buildWorkbook(t, sampleRows(), false)
	rows, err := f.GetRows("Dữ liệu")
	if err != nil {
		t.Fatalf("GetRows: %v", err)
	}

	col := columnIndex(t, rows[0], "Sức khoẻ")
	if got := rows[1][col]; got != Masked {
		t.Errorf("sensitive cell = %q, want %q", got, Masked)
	}
	for _, row := range rows {
		if strings.Contains(strings.Join(row, "|"), "tiểu đường") {
			t.Fatal("a sensitive answer was written in clear despite masking")
		}
	}
}

func TestSensitiveShownWhenEntitled(t *testing.T) {
	t.Parallel()

	f := buildWorkbook(t, sampleRows(), true)
	rows, _ := f.GetRows("Dữ liệu")
	col := columnIndex(t, rows[0], "Sức khoẻ")

	if got := rows[1][col]; got != "tiểu đường type 2" {
		t.Errorf("sensitive cell = %q, want the real answer", got)
	}
}

// TestThreeEmptyStatesAreDistinct pins the distinction the whole grid design
// rests on. Collapsing them turns "never offered" into "skipped".
func TestThreeEmptyStatesAreDistinct(t *testing.T) {
	t.Parallel()

	f := buildWorkbook(t, sampleRows(), true)
	rows, _ := f.GetRows("Dữ liệu")
	health := columnIndex(t, rows[0], "Sức khoẻ")
	used := columnIndex(t, rows[0], "Đã dùng?")

	if got := rows[2][health]; got != markNotAsked {
		t.Errorf("not-asked cell = %q, want %q", got, markNotAsked)
	}
	if got := rows[3][health]; got != markHidden {
		t.Errorf("hidden cell = %q, want %q", got, markHidden)
	}
	if got := rows[2][used]; got != "" {
		t.Errorf("blank cell = %q, want empty", got)
	}
}

// TestOptionIdsBecomeLabels: answers are stored by id so relabelling cannot
// corrupt them, and the translation to text happens here.
func TestOptionIdsBecomeLabels(t *testing.T) {
	t.Parallel()

	f := buildWorkbook(t, sampleRows(), true)
	rows, _ := f.GetRows("Dữ liệu")
	used := columnIndex(t, rows[0], "Đã dùng?")

	if got := rows[1][used]; got != "Rồi" {
		t.Errorf("choice cell = %q, want the label \"Rồi\", not the stored id", got)
	}
}

func TestProvenanceSheetRecordsTheImportantFacts(t *testing.T) {
	t.Parallel()

	f := buildWorkbook(t, sampleRows(), false)
	rows, err := f.GetRows("Thông tin xuất")
	if err != nil {
		t.Fatalf("GetRows: %v", err)
	}
	text := strings.Join(flatten(rows), "\n")

	for _, want := range []string{
		"a@acme.vn",       // who
		"Đã che",          // whether sensitive data is readable
		"DỮ LIỆU CÁ NHÂN", // what the file is
		markNotAsked,      // how to read the blanks
		markHidden,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the provenance sheet does not mention %q", want)
		}
	}
}

// TestExcludedRowsAreDeclared: rows dropped for compliance reasons must show up,
// or the report reads as complete when it is not.
func TestExcludedRowsAreDeclared(t *testing.T) {
	t.Parallel()

	f := buildWorkbook(t, sampleRows(), false)
	rows, _ := f.GetRows("Tổng quan")
	text := strings.Join(flatten(rows), "\n")

	if !strings.Contains(text, "Số bản ghi bị loại") || !strings.Contains(text, "3") {
		t.Error("the summary does not declare how many rows were excluded")
	}
}

func TestQuestionSheetStatesItsDenominator(t *testing.T) {
	t.Parallel()

	f := buildWorkbook(t, sampleRows(), false)
	rows, _ := f.GetRows("Theo câu hỏi")
	text := strings.Join(flatten(rows), "\n")

	// Anyone reading percentages needs to know what they are percentages of.
	if !strings.Contains(text, "THỰC SỰ được hiển thị") {
		t.Error("the question sheet does not state what its percentages are computed over")
	}
}

func TestEmptyExportStillProducesAValidWorkbook(t *testing.T) {
	t.Parallel()

	f := buildWorkbook(t, nil, false)
	sheets := f.GetSheetList()
	for _, want := range []string{"Tổng quan", "Dữ liệu", "Theo câu hỏi", "Thông tin xuất"} {
		if !contains(sheets, want) {
			t.Errorf("sheet %q is missing from %v", want, sheets)
		}
	}
}

func columnIndex(t *testing.T, header []string, label string) int {
	t.Helper()
	for i, h := range header {
		if strings.HasPrefix(h, label) {
			return i
		}
	}
	t.Fatalf("no column %q in header %v", label, header)
	return -1
}

func flatten(rows [][]string) []string {
	var out []string
	for _, r := range rows {
		out = append(out, strings.Join(r, " | "))
	}
	return out
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
