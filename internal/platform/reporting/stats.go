// Package reporting turns submissions into statistics.
//
// Pure computation: it takes rows and returns numbers, with no database and no
// clock. That makes the arithmetic testable, and the arithmetic is where reports
// go wrong -- almost always by dividing by the wrong thing.
package reporting

import (
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/collectr/collectr/internal/contracts"
)

// QuestionStats summarises the answers to one question.
//
// Answered and Shown are both here because their ratio is the only honest
// completion figure for a branching form.
type QuestionStats struct {
	FieldID string
	Label   string
	Type    string

	// Shown is how many respondents actually saw the question.
	//
	// This, not the total number of submissions, is the denominator for every
	// percentage below. Using the total would make every question behind a
	// branch look abandoned when it was simply never offered.
	Shown int
	// Answered is how many of those gave an answer.
	Answered int
	// NotAsked and Hidden explain the rest of the population.
	NotAsked int
	Hidden   int

	// Choice and multi-choice.
	Options []OptionCount
	// Multi-choice only: the average number of options ticked.
	MeanSelections float64

	// Rating.
	Mean      float64
	Median    float64
	Histogram map[int]int
	// LowShare is the fraction rating 2 or below -- the group worth calling.
	LowShare float64

	// Text.
	MedianLength int
	// Date.
	Earliest string
	Latest   string
	// File.
	AttachedShare float64
}

// AnswerRate is answers over people who were shown the question.
func (q QuestionStats) AnswerRate() float64 {
	if q.Shown == 0 {
		return 0
	}
	return float64(q.Answered) / float64(q.Shown)
}

// OptionCount is one choice and how often it was picked.
type OptionCount struct {
	ID    string
	Label string
	Count int
	Share float64
}

// ConsentStats summarises agreement to one processing purpose.
type ConsentStats struct {
	Purpose string
	Granted int
	Total   int
}

// Share is the fraction who agreed.
//
// The number that decides what a collected list is actually worth: a thousand
// contacts with a 15% marketing consent rate is a list of a hundred and fifty.
func (c ConsentStats) Share() float64 {
	if c.Total == 0 {
		return 0
	}
	return float64(c.Granted) / float64(c.Total)
}

// Report is everything computed about one form over a period.
type Report struct {
	FormTitle string
	From      time.Time
	To        time.Time

	Submissions int
	// Excluded counts rows left out because they were erased, restricted, or
	// belong to someone who withdrew consent. Always reported: silently dropping
	// them makes a report read as complete when it is not.
	Excluded int

	Funnel    contracts.FunnelSummary
	DropOff   []contracts.PageDropOff
	Questions []QuestionStats
	Consents  []ConsentStats

	Devices   []LabelCount
	Countries []LabelCount
	Sources   []LabelCount
	ByDay     []DayCount

	Versions []int
}

// LabelCount is one breakdown entry.
type LabelCount struct {
	Label string
	Count int
}

// DayCount is submissions on one day.
type DayCount struct {
	Day   time.Time
	Count int
}

// Accumulator builds a Report row by row, so a report never needs the whole
// result set in memory at once.
type Accumulator struct {
	columns []contracts.ExportColumn

	submissions int
	shown       map[string]int
	answered    map[string]int
	notAsked    map[string]int
	hidden      map[string]int

	choices     map[string]map[string]int
	selections  map[string][]int
	ratings     map[string][]float64
	textLengths map[string][]int
	dates       map[string][]string
	files       map[string]int

	consentGranted map[string]int
	consentTotal   map[string]int

	devices   map[string]int
	countries map[string]int
	sources   map[string]int
	byDay     map[string]int
	versions  map[int]struct{}
}

// NewAccumulator returns an Accumulator for a form's column set.
func NewAccumulator(columns []contracts.ExportColumn) *Accumulator {
	return &Accumulator{
		columns:        columns,
		shown:          map[string]int{},
		answered:       map[string]int{},
		notAsked:       map[string]int{},
		hidden:         map[string]int{},
		choices:        map[string]map[string]int{},
		selections:     map[string][]int{},
		ratings:        map[string][]float64{},
		textLengths:    map[string][]int{},
		dates:          map[string][]string{},
		files:          map[string]int{},
		consentGranted: map[string]int{},
		consentTotal:   map[string]int{},
		devices:        map[string]int{},
		countries:      map[string]int{},
		sources:        map[string]int{},
		byDay:          map[string]int{},
		versions:       map[int]struct{}{},
	}
}

// Add folds one submission into the running totals.
func (a *Accumulator) Add(row contracts.ExportRow) {
	a.submissions++
	a.versions[row.VersionNo] = struct{}{}
	a.byDay[row.SubmittedAt.UTC().Format(time.DateOnly)]++
	bump(a.devices, orUnknown(row.Device))
	bump(a.countries, orUnknown(row.Country))
	bump(a.sources, orUnknown(row.SourceLink))

	for purpose, granted := range row.Consents {
		a.consentTotal[purpose]++
		if granted {
			a.consentGranted[purpose]++
		}
	}

	for _, col := range a.columns {
		cell, ok := row.Cells[col.FieldID]
		if !ok {
			a.notAsked[col.FieldID]++
			continue
		}
		switch cell.State {
		case contracts.CellNotAsked:
			a.notAsked[col.FieldID]++
			continue
		case contracts.CellHidden:
			a.hidden[col.FieldID]++
			continue
		}

		// Shown covers blank as well as answered: someone who saw the question
		// and skipped it belongs in the denominator.
		a.shown[col.FieldID]++
		if cell.State != contracts.CellAnswered {
			continue
		}
		a.answered[col.FieldID]++
		a.record(col, cell.Value)
	}
}

// record accumulates one answered value according to its field type.
func (a *Accumulator) record(col contracts.ExportColumn, v any) {
	switch col.Type {
	case "choice", "dropdown":
		if s, ok := v.(string); ok {
			if a.choices[col.FieldID] == nil {
				a.choices[col.FieldID] = map[string]int{}
			}
			a.choices[col.FieldID][s]++
		}
	case "multi_choice":
		list, ok := v.([]any)
		if !ok {
			return
		}
		if a.choices[col.FieldID] == nil {
			a.choices[col.FieldID] = map[string]int{}
		}
		for _, item := range list {
			if s, ok := item.(string); ok {
				a.choices[col.FieldID][s]++
			}
		}
		a.selections[col.FieldID] = append(a.selections[col.FieldID], len(list))
	case "rating":
		if n, ok := toFloat(v); ok {
			a.ratings[col.FieldID] = append(a.ratings[col.FieldID], n)
		}
	case "text":
		if s, ok := v.(string); ok {
			a.textLengths[col.FieldID] = append(a.textLengths[col.FieldID], len([]rune(s)))
		}
	case "date":
		if s, ok := v.(string); ok && s != "" {
			a.dates[col.FieldID] = append(a.dates[col.FieldID], s)
		}
	case "file":
		a.files[col.FieldID]++
	}
}

// Build produces the finished report.
func (a *Accumulator) Build(title string, from, to time.Time, funnel contracts.FunnelSummary, dropOff []contracts.PageDropOff, excluded int) Report {
	r := Report{
		FormTitle: title, From: from, To: to,
		Submissions: a.submissions, Excluded: excluded,
		Funnel: funnel, DropOff: dropOff,
	}

	for _, col := range a.columns {
		q := QuestionStats{
			FieldID: col.FieldID, Label: col.Label, Type: col.Type,
			Shown: a.shown[col.FieldID], Answered: a.answered[col.FieldID],
			NotAsked: a.notAsked[col.FieldID], Hidden: a.hidden[col.FieldID],
		}

		switch col.Type {
		case "choice", "dropdown", "multi_choice":
			counts := a.choices[col.FieldID]
			// Every declared option appears, including ones nobody picked: a
			// zero is a finding, and omitting it hides that the option existed.
			for _, opt := range col.Options {
				c := counts[opt.ID]
				q.Options = append(q.Options, OptionCount{
					ID: opt.ID, Label: opt.Label, Count: c,
					Share: ratio(c, q.Shown),
				})
			}
			// Ids stored under an option that has since been removed still have
			// to be reported, or the totals will not add up.
			for id, c := range counts {
				if !slices.ContainsFunc(col.Options, func(o contracts.ExportOption) bool { return o.ID == id }) {
					q.Options = append(q.Options, OptionCount{
						ID: id, Label: id + " (đã gỡ)", Count: c, Share: ratio(c, q.Shown),
					})
				}
			}
			sort.SliceStable(q.Options, func(i, j int) bool { return q.Options[i].Count > q.Options[j].Count })
			if sel := a.selections[col.FieldID]; len(sel) > 0 {
				var sum int
				for _, n := range sel {
					sum += n
				}
				q.MeanSelections = float64(sum) / float64(len(sel))
			}

		case "rating":
			values := a.ratings[col.FieldID]
			if len(values) > 0 {
				q.Mean = mean(values)
				q.Median = median(values)
				q.Histogram = map[int]int{}
				var low int
				for _, v := range values {
					q.Histogram[int(math.Round(v))]++
					if v <= 2 {
						low++
					}
				}
				q.LowShare = ratio(low, len(values))
			}

		case "text":
			if lengths := a.textLengths[col.FieldID]; len(lengths) > 0 {
				floats := make([]float64, len(lengths))
				for i, n := range lengths {
					floats[i] = float64(n)
				}
				q.MedianLength = int(median(floats))
			}

		case "date":
			if dates := a.dates[col.FieldID]; len(dates) > 0 {
				sorted := slices.Clone(dates)
				slices.Sort(sorted) // ISO-8601 sorts correctly as text
				q.Earliest, q.Latest = sorted[0], sorted[len(sorted)-1]
			}

		case "file":
			q.AttachedShare = ratio(a.files[col.FieldID], q.Shown)
		}

		r.Questions = append(r.Questions, q)
	}

	for purpose, total := range a.consentTotal {
		r.Consents = append(r.Consents, ConsentStats{
			Purpose: purpose, Granted: a.consentGranted[purpose], Total: total,
		})
	}
	sort.Slice(r.Consents, func(i, j int) bool { return r.Consents[i].Purpose < r.Consents[j].Purpose })

	r.Devices = topN(a.devices, 10)
	r.Countries = topN(a.countries, 10)
	r.Sources = topN(a.sources, 10)

	for day, count := range a.byDay {
		d, err := time.Parse(time.DateOnly, day)
		if err != nil {
			continue
		}
		r.ByDay = append(r.ByDay, DayCount{Day: d, Count: count})
	}
	sort.Slice(r.ByDay, func(i, j int) bool { return r.ByDay[i].Day.Before(r.ByDay[j].Day) })

	for v := range a.versions {
		r.Versions = append(r.Versions, v)
	}
	slices.Sort(r.Versions)

	return r
}

func bump(m map[string]int, key string) { m[key]++ }

func orUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(không rõ)"
	}
	return s
}

func ratio(n, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(n) / float64(total)
}

func mean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	var sum float64
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}

// median is used instead of the mean wherever one outlier would distort the
// answer -- time spent, text length. A tab left open overnight should not move
// the reported typical case.
func median(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := slices.Clone(values)
	slices.Sort(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}

func topN(m map[string]int, n int) []LabelCount {
	out := make([]LabelCount, 0, len(m))
	for label, count := range m {
		out = append(out, LabelCount{Label: label, Count: count})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Label < out[j].Label
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

func toFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case int:
		return float64(t), true
	default:
		return 0, false
	}
}
