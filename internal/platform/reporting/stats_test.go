package reporting

import (
	"math"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/collectr/collectr/internal/contracts"
)

func columns() []contracts.ExportColumn {
	return []contracts.ExportColumn{
		{FieldID: "f_used", Label: "Đã dùng?", Type: "choice", Options: []contracts.ExportOption{
			{ID: "o_yes", Label: "Rồi"}, {ID: "o_no", Label: "Chưa"},
		}},
		{FieldID: "f_rating", Label: "Đánh giá", Type: "rating"},
		{FieldID: "f_note", Label: "Ghi chú", Type: "text"},
	}
}

func row(cells map[string]contracts.ExportCell) contracts.ExportRow {
	return contracts.ExportRow{
		ID: uuid.New(), VersionNo: 1, SubmittedAt: time.Now().UTC(),
		Status: "active", Cells: cells,
	}
}

func answered(v any) contracts.ExportCell {
	return contracts.ExportCell{State: contracts.CellAnswered, Value: v}
}

// TestDenominatorIsPeopleWhoSawTheQuestion is the arithmetic that matters most.
//
// On a branching form, most questions are shown to a minority of respondents.
// Dividing by the total number of submissions instead would report every one of
// them as mostly unanswered, which is both wrong and the kind of wrong nobody
// notices until a decision has been made on it.
func TestDenominatorIsPeopleWhoSawTheQuestion(t *testing.T) {
	t.Parallel()

	a := NewAccumulator(columns())
	// Ten submissions; only two ever reached the rating question, and both
	// answered it.
	for range 8 {
		a.Add(row(map[string]contracts.ExportCell{
			"f_used":   answered("o_no"),
			"f_rating": {State: contracts.CellHidden},
			"f_note":   {State: contracts.CellHidden},
		}))
	}
	for range 2 {
		a.Add(row(map[string]contracts.ExportCell{
			"f_used":   answered("o_yes"),
			"f_rating": answered(float64(5)),
			"f_note":   {State: contracts.CellBlank},
		}))
	}

	rep := a.Build("F", time.Time{}, time.Time{}, contracts.FunnelSummary{}, nil, 0)
	rating := findQuestion(t, rep, "f_rating")

	if rating.Shown != 2 {
		t.Errorf("Shown = %d, want 2", rating.Shown)
	}
	if rating.Hidden != 8 {
		t.Errorf("Hidden = %d, want 8", rating.Hidden)
	}
	if got := rating.AnswerRate(); got != 1.0 {
		t.Errorf("AnswerRate = %.2f, want 1.00 -- everyone who saw it answered it", got)
	}

	note := findQuestion(t, rep, "f_note")
	if note.Shown != 2 || note.Answered != 0 {
		t.Errorf("note Shown/Answered = %d/%d, want 2/0", note.Shown, note.Answered)
	}
	if got := note.AnswerRate(); got != 0 {
		t.Errorf("note AnswerRate = %.2f, want 0 -- shown twice, skipped twice", got)
	}
}

func TestNotAskedIsSeparateFromHidden(t *testing.T) {
	t.Parallel()

	a := NewAccumulator(columns())
	a.Add(row(map[string]contracts.ExportCell{
		"f_used":   answered("o_yes"),
		"f_rating": {State: contracts.CellNotAsked}, // an older version lacked it
	}))
	a.Add(row(map[string]contracts.ExportCell{
		"f_used":   answered("o_yes"),
		"f_rating": {State: contracts.CellHidden}, // a branch skipped it
	}))

	rep := a.Build("F", time.Time{}, time.Time{}, contracts.FunnelSummary{}, nil, 0)
	rating := findQuestion(t, rep, "f_rating")

	if rating.NotAsked != 1 || rating.Hidden != 1 || rating.Shown != 0 {
		t.Errorf("NotAsked/Hidden/Shown = %d/%d/%d, want 1/1/0",
			rating.NotAsked, rating.Hidden, rating.Shown)
	}
}

func TestChoiceCounts(t *testing.T) {
	t.Parallel()

	a := NewAccumulator(columns())
	for range 3 {
		a.Add(row(map[string]contracts.ExportCell{"f_used": answered("o_yes")}))
	}
	a.Add(row(map[string]contracts.ExportCell{"f_used": answered("o_no")}))

	rep := a.Build("F", time.Time{}, time.Time{}, contracts.FunnelSummary{}, nil, 0)
	used := findQuestion(t, rep, "f_used")

	if len(used.Options) != 2 {
		t.Fatalf("got %d options, want 2", len(used.Options))
	}
	if used.Options[0].ID != "o_yes" || used.Options[0].Count != 3 {
		t.Errorf("top option = %+v, want o_yes with 3", used.Options[0])
	}
	if got := used.Options[0].Share; math.Abs(got-0.75) > 1e-9 {
		t.Errorf("share = %.3f, want 0.75", got)
	}
}

// TestUnpickedOptionsStillAppear: a zero is a finding. Dropping the row hides
// that the option was ever offered.
func TestUnpickedOptionsStillAppear(t *testing.T) {
	t.Parallel()

	a := NewAccumulator(columns())
	a.Add(row(map[string]contracts.ExportCell{"f_used": answered("o_yes")}))

	rep := a.Build("F", time.Time{}, time.Time{}, contracts.FunnelSummary{}, nil, 0)
	used := findQuestion(t, rep, "f_used")

	var found bool
	for _, o := range used.Options {
		if o.ID == "o_no" {
			found = true
			if o.Count != 0 {
				t.Errorf("o_no count = %d, want 0", o.Count)
			}
		}
	}
	if !found {
		t.Error("an option nobody picked was omitted from the report")
	}
}

// TestRemovedOptionsAreStillCounted: answers stored under an option that has
// since been deleted must still show up, or the totals silently stop adding up.
func TestRemovedOptionsAreStillCounted(t *testing.T) {
	t.Parallel()

	a := NewAccumulator(columns())
	a.Add(row(map[string]contracts.ExportCell{"f_used": answered("o_gone")}))

	rep := a.Build("F", time.Time{}, time.Time{}, contracts.FunnelSummary{}, nil, 0)
	used := findQuestion(t, rep, "f_used")

	var total int
	for _, o := range used.Options {
		total += o.Count
	}
	if total != 1 {
		t.Errorf("counted %d answers, want 1: a retired option was dropped", total)
	}
}

func TestRatingStats(t *testing.T) {
	t.Parallel()

	a := NewAccumulator(columns())
	for _, v := range []float64{1, 2, 3, 4, 5, 5} {
		a.Add(row(map[string]contracts.ExportCell{"f_rating": answered(v)}))
	}

	rep := a.Build("F", time.Time{}, time.Time{}, contracts.FunnelSummary{}, nil, 0)
	q := findQuestion(t, rep, "f_rating")

	if math.Abs(q.Mean-20.0/6) > 1e-9 {
		t.Errorf("Mean = %.4f, want %.4f", q.Mean, 20.0/6)
	}
	if q.Median != 3.5 {
		t.Errorf("Median = %.2f, want 3.50", q.Median)
	}
	if got := q.LowShare; math.Abs(got-2.0/6) > 1e-9 {
		t.Errorf("LowShare = %.4f, want %.4f", got, 2.0/6)
	}
	if q.Histogram[5] != 2 {
		t.Errorf("histogram[5] = %d, want 2", q.Histogram[5])
	}
}

func TestConsentShare(t *testing.T) {
	t.Parallel()

	a := NewAccumulator(columns())
	for i := range 10 {
		r := row(map[string]contracts.ExportCell{})
		r.Consents = map[string]bool{"service": true, "marketing": i < 3}
		a.Add(r)
	}

	rep := a.Build("F", time.Time{}, time.Time{}, contracts.FunnelSummary{}, nil, 0)
	for _, c := range rep.Consents {
		switch c.Purpose {
		case "service":
			if c.Share() != 1.0 {
				t.Errorf("service share = %.2f, want 1.00", c.Share())
			}
		case "marketing":
			if math.Abs(c.Share()-0.3) > 1e-9 {
				t.Errorf("marketing share = %.2f, want 0.30", c.Share())
			}
		}
	}
}

func TestFunnelRates(t *testing.T) {
	t.Parallel()

	f := contracts.FunnelSummary{Clicks: 1000, Views: 600, Starts: 400, Submits: 100}

	if got := f.CompletionRate(); math.Abs(got-100.0/600) > 1e-9 {
		t.Errorf("CompletionRate = %.4f, want %.4f", got, 100.0/600)
	}
	// Started and gave up is a different problem from never started; the two
	// rates are deliberately not the same number.
	if got := f.AbandonRate(); math.Abs(got-0.75) > 1e-9 {
		t.Errorf("AbandonRate = %.4f, want 0.75", got)
	}

	empty := contracts.FunnelSummary{}
	if empty.CompletionRate() != 0 || empty.AbandonRate() != 0 {
		t.Error("an empty funnel must not divide by zero")
	}
}

func TestExcludedIsReported(t *testing.T) {
	t.Parallel()

	a := NewAccumulator(columns())
	a.Add(row(map[string]contracts.ExportCell{"f_used": answered("o_yes")}))

	rep := a.Build("F", time.Time{}, time.Time{}, contracts.FunnelSummary{}, nil, 7)
	if rep.Excluded != 7 {
		t.Errorf("Excluded = %d, want 7: rows dropped for compliance reasons must be visible", rep.Excluded)
	}
}

func TestPageDropOffRate(t *testing.T) {
	t.Parallel()

	p := contracts.PageDropOff{PageID: "p2", Entered: 200, Left: 50}
	if math.Abs(p.Rate()-0.25) > 1e-9 {
		t.Errorf("Rate = %.4f, want 0.25", p.Rate())
	}
	if (contracts.PageDropOff{}).Rate() != 0 {
		t.Error("an empty page must not divide by zero")
	}
}

func findQuestion(t *testing.T, r Report, fieldID string) QuestionStats {
	t.Helper()
	for _, q := range r.Questions {
		if q.FieldID == fieldID {
			return q
		}
	}
	t.Fatalf("no stats for field %q", fieldID)
	return QuestionStats{}
}
