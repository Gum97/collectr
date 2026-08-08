// Package engine evaluates a form's conditional logic.
//
// Evaluate is a pure function: no I/O, no clock, no randomness. That is what
// lets the same logic compile to WebAssembly for the browser while the server
// keeps the authoritative copy, with one shared set of golden fixtures keeping
// the two from drifting apart.
//
// The server must always re-evaluate. Trusting the client's claim about which
// fields it displayed would let a caller submit answers for a branch they never
// saw -- and therefore never saw the consent text for.
package engine

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/collectr/collectr/internal/modules/forms/domain"
)

// Result is the outcome of evaluating a schema against a set of answers.
type Result struct {
	// Visible lists the fields actually shown, in page then declaration order.
	Visible []domain.FieldID
	// Required lists the visible fields that must be answered.
	Required []domain.FieldID
	// Path lists the pages traversed, in order.
	Path []domain.PageID
}

// IsVisible reports whether the field was shown.
func (r Result) IsVisible(id domain.FieldID) bool {
	for _, f := range r.Visible {
		if f == id {
			return true
		}
	}
	return false
}

// Evaluate walks the form as the respondent's answers direct, and reports which
// fields they were actually shown.
func Evaluate(s domain.Schema, answers map[string]any) (Result, error) {
	if len(s.Pages) == 0 {
		return Result{}, nil
	}

	// Rules are grouped by page once, so the walk stays linear rather than
	// rescanning every rule for every page.
	byPage := make(map[domain.PageID][]domain.Rule, len(s.Pages))
	for _, r := range s.Rules {
		byPage[r.OnPage] = append(byPage[r.OnPage], r)
	}

	var (
		res     Result
		current = s.Pages[0].ID
		seen    = make(map[domain.PageID]struct{}, len(s.Pages))
	)

	// Publishing rejects cyclic navigation, but a schema written before that
	// check existed -- or one edited straight in the database -- must not be able
	// to hang a request. The visited set bounds the walk regardless.
	for range len(s.Pages) + 1 {
		page, ok := s.Page(current)
		if !ok {
			return Result{}, fmt.Errorf("page %q referenced but not defined", current)
		}
		if _, repeat := seen[current]; repeat {
			return Result{}, fmt.Errorf("navigation cycle at page %q", current)
		}
		seen[current] = struct{}{}
		res.Path = append(res.Path, current)

		visible, required, next, err := evalPage(s, page, byPage[current], answers)
		if err != nil {
			return Result{}, err
		}
		res.Visible = append(res.Visible, visible...)
		res.Required = append(res.Required, required...)

		if next == "" {
			return res, nil
		}
		current = next
	}
	return Result{}, fmt.Errorf("navigation exceeded %d pages", len(s.Pages))
}

// evalPage resolves one page: which of its fields are shown, which are required,
// and where to go next. next == "" means the form ends here.
func evalPage(s domain.Schema, page domain.Page, rules []domain.Rule, answers map[string]any) (visible, required []domain.FieldID, next domain.PageID, err error) {
	// Start from the declared state, then let rules amend it. A field marked
	// hidden appears only if a rule shows it.
	shown := make(map[domain.FieldID]bool, len(page.Fields))
	must := make(map[domain.FieldID]bool, len(page.Fields))
	for _, fid := range page.Fields {
		f, ok := s.Fields[fid]
		if !ok {
			return nil, nil, "", fmt.Errorf("page %q lists undefined field %q", page.ID, fid)
		}
		shown[fid] = !f.Hidden
		must[fid] = f.Required
	}

	var (
		jump    domain.PageID
		ended   bool
		decided bool
	)

	// One pass, in declaration order. Later rules override earlier ones, which
	// makes the outcome depend only on the schema's own ordering -- no fixpoint
	// iteration, no dependence on map order.
	for _, rule := range rules {
		match, err := evalCondition(rule.When, answers)
		if err != nil {
			return nil, nil, "", fmt.Errorf("rule %q: %w", rule.ID, err)
		}
		actions := rule.Else
		if match {
			actions = rule.Then
		}
		for _, a := range actions {
			switch a.Action {
			case domain.ActionShow:
				shown[domain.FieldID(a.Target)] = true
			case domain.ActionHide:
				shown[domain.FieldID(a.Target)] = false
			case domain.ActionRequire:
				must[domain.FieldID(a.Target)] = true
			case domain.ActionOptional:
				must[domain.FieldID(a.Target)] = false
			case domain.ActionGoto:
				jump, ended, decided = domain.PageID(a.Target), false, true
			case domain.ActionEnd:
				jump, ended, decided = "", true, true
			default:
				return nil, nil, "", fmt.Errorf("rule %q: unknown action %q", rule.ID, a.Action)
			}
		}
	}

	for _, fid := range page.Fields {
		if !shown[fid] {
			continue
		}
		visible = append(visible, fid)
		if must[fid] {
			required = append(required, fid)
		}
	}

	switch {
	case decided && ended:
		return visible, required, "", nil
	case decided:
		return visible, required, jump, nil
	default:
		return visible, required, defaultNext(s, page.ID), nil
	}
}

// defaultNext returns where the form goes when no rule decides: the page's
// declared Next, or the following page in order.
func defaultNext(s domain.Schema, id domain.PageID) domain.PageID {
	for i, p := range s.Pages {
		if p.ID != id {
			continue
		}
		if p.Next != "" {
			return p.Next
		}
		if i+1 < len(s.Pages) {
			return s.Pages[i+1].ID
		}
		return ""
	}
	return ""
}

// evalCondition resolves one comparison.
//
// An unanswered field makes every comparison false except the emptiness checks.
// The alternative -- treating a missing answer as a zero value -- would silently
// route respondents down the "rating is 0, so ask what went wrong" branch before
// they had rated anything.
func evalCondition(c domain.Condition, answers map[string]any) (bool, error) {
	raw, answered := answers[string(c.Field)]
	if answered {
		answered = !isEmpty(raw)
	}

	switch c.Op {
	case domain.OpIsEmpty:
		return !answered, nil
	case domain.OpIsNotEmpty:
		return answered, nil
	}
	if !answered {
		return false, nil
	}

	switch c.Op {
	case domain.OpEq:
		return equal(raw, c.Value), nil
	case domain.OpNeq:
		return !equal(raw, c.Value), nil
	case domain.OpIn:
		return inList(raw, c.Value), nil
	case domain.OpNotIn:
		return !inList(raw, c.Value), nil
	case domain.OpContains:
		return contains(raw, c.Value), nil
	case domain.OpGt, domain.OpGte, domain.OpLt, domain.OpLte:
		return compareOrder(c.Op, raw, c.Value)
	case domain.OpBetween:
		bounds, ok := c.Value.([]any)
		if !ok || len(bounds) != 2 {
			return false, fmt.Errorf("operator %q needs a two-element value", domain.OpBetween)
		}
		lo, err := compareOrder(domain.OpGte, raw, bounds[0])
		if err != nil {
			return false, err
		}
		hi, err := compareOrder(domain.OpLte, raw, bounds[1])
		if err != nil {
			return false, err
		}
		return lo && hi, nil
	default:
		return false, fmt.Errorf("unknown operator %q", c.Op)
	}
}

func isEmpty(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(t) == ""
	case []any:
		return len(t) == 0
	case []string:
		return len(t) == 0
	case map[string]any:
		return len(t) == 0
	default:
		return false
	}
}

// equal compares an answer against a rule value.
//
// Both sides arrive from JSON, where every number is a float64 and every choice
// is an option id string, so comparison normalises to string except when both
// sides are genuinely numeric.
func equal(answer, want any) bool {
	if an, ok := asNumber(answer); ok {
		if wn, ok := asNumber(want); ok {
			return an == wn
		}
	}
	return asString(answer) == asString(want)
}

func inList(answer, want any) bool {
	list, ok := want.([]any)
	if !ok {
		return false
	}
	for _, item := range list {
		if equal(answer, item) {
			return true
		}
	}
	return false
}

// contains checks membership for multi-select answers and substring for text.
func contains(answer, want any) bool {
	switch t := answer.(type) {
	case []any:
		for _, item := range t {
			if equal(item, want) {
				return true
			}
		}
		return false
	case []string:
		for _, item := range t {
			if equal(item, want) {
				return true
			}
		}
		return false
	default:
		return strings.Contains(asString(answer), asString(want))
	}
}

func compareOrder(op string, answer, want any) (bool, error) {
	an, aok := asNumber(answer)
	wn, wok := asNumber(want)
	if aok && wok {
		return applyOrder(op, cmpFloat(an, wn)), nil
	}
	// Dates are ISO-8601, which sorts correctly as text, so ordering still works
	// for date fields without parsing them.
	as, ws := asString(answer), asString(want)
	if as == "" || ws == "" {
		return false, nil
	}
	return applyOrder(op, strings.Compare(as, ws)), nil
}

func applyOrder(op string, c int) bool {
	switch op {
	case domain.OpGt:
		return c > 0
	case domain.OpGte:
		return c >= 0
	case domain.OpLt:
		return c < 0
	case domain.OpLte:
		return c <= 0
	default:
		return false
	}
}

func cmpFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

func asNumber(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case float32:
		return float64(t), true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		return f, err == nil
	default:
		return 0, false
	}
}

func asString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return fmt.Sprint(t)
	}
}
