package domain

import (
	"fmt"
	"slices"
	"strings"
)

// Issue is one problem found in a draft schema.
type Issue struct {
	Code     string `json:"code"`
	Severity string `json:"severity"` // error | warning
	Target   string `json:"target,omitempty"`
	Message  string `json:"message"`
}

// Issue codes.
const (
	IssueDanglingRef     = "dangling_reference"
	IssueCycle           = "navigation_cycle"
	IssueUnreachable     = "unreachable_page"
	IssueRequiredHidden  = "required_but_unreachable"
	IssueConsentMissing  = "consent_block_missing"
	IssueSensitiveNotice = "sensitive_notice_missing"
	IssueLimitExceeded   = "limit_exceeded"
	IssueEmptySchema     = "empty_schema"
	IssueDuplicateID     = "duplicate_id"
	IssueOrphanField     = "orphan_field"
	IssueBadFieldConfig  = "invalid_field_config"
)

// Severities.
const (
	SeverityError   = "error"
	SeverityWarning = "warning"
)

// ValidationResult is the outcome of checking a draft.
type ValidationResult struct {
	OK     bool    `json:"ok"`
	Issues []Issue `json:"issues,omitempty"`
}

// Validate checks a draft schema before it may be published.
//
// Publishing is the last moment these problems are cheap. Afterwards the version
// is immutable and any respondent who hits a broken branch is a lost submission
// that nobody will report -- they simply close the tab.
func Validate(s Schema) ValidationResult {
	var issues []Issue
	add := func(code, severity, target, format string, args ...any) {
		issues = append(issues, Issue{
			Code:     code,
			Severity: severity,
			Target:   target,
			Message:  fmt.Sprintf(format, args...),
		})
	}

	if len(s.Pages) == 0 {
		add(IssueEmptySchema, SeverityError, "", "a form needs at least one page")
		return ValidationResult{OK: false, Issues: issues}
	}

	// 1. Limits, checked first: everything below is O(pages x rules), and a
	//    schema over the cap is rejected regardless of what else it contains.
	limits := s.EffectiveLimits()
	if len(s.Fields) > limits.MaxFields {
		add(IssueLimitExceeded, SeverityError, "", "form has %d fields, the limit is %d", len(s.Fields), limits.MaxFields)
	}
	if len(s.Rules) > limits.MaxRules {
		add(IssueLimitExceeded, SeverityError, "", "form has %d rules, the limit is %d", len(s.Rules), limits.MaxRules)
	}

	// 2. Structural integrity: unique ids, no field on two pages, no orphans.
	// A form that asks for consent must say which answer identifies the person
	// giving it.
	//
	// Without it the submit path cannot attach the consent record to anybody and
	// refuses at the last moment, so the respondent -- who did nothing wrong --
	// is told the form cannot accept responses while the person who could fix it
	// never hears about it. Refused here instead, where publishing is the act
	// that would have shipped the fault.
	if len(s.Consent.Purposes) > 0 {
		identified := false
		for _, f := range s.Fields {
			if f.Identifier {
				identified = true
				break
			}
		}
		if !identified {
			add(IssueBadFieldConfig, SeverityError, "",
				"form collects consent but marks no field as the data subject "+
					"identifier, so a consent record cannot be attached to anyone")
		}
	}

	pageIDs := make(map[PageID]int, len(s.Pages))
	fieldPage := make(map[FieldID]PageID, len(s.Fields))
	for _, p := range s.Pages {
		pageIDs[p.ID]++
		if pageIDs[p.ID] > 1 {
			add(IssueDuplicateID, SeverityError, string(p.ID), "page id %q is used more than once", p.ID)
		}
		if p.Next != "" {
			if _, ok := pageIDs[p.Next]; !ok {
				// pageIDs is still being built, so recheck against the full list.
				if _, defined := s.Page(p.Next); !defined {
					add(IssueDanglingRef, SeverityError, string(p.ID),
						"page %q continues to page %q, which is not defined", p.ID, p.Next)
				}
			}
		}
		for _, fid := range p.Fields {
			if !s.HasField(fid) {
				add(IssueDanglingRef, SeverityError, string(fid), "page %q lists field %q, which is not defined", p.ID, fid)
				continue
			}
			if prev, dup := fieldPage[fid]; dup {
				add(IssueDuplicateID, SeverityError, string(fid),
					"field %q appears on both page %q and page %q", fid, prev, p.ID)
				continue
			}
			fieldPage[fid] = p.ID
		}
	}
	for fid, f := range s.Fields {
		if _, placed := fieldPage[fid]; !placed {
			add(IssueOrphanField, SeverityWarning, string(fid),
				"field %q is defined but not placed on any page, so it can never be answered", fid)
		}
		issues = append(issues, validateField(fid, f)...)
	}

	// 3. Dangling references in rules. Checked before the graph walk, because a
	//    rule pointing at a missing page would otherwise look like a dead end.
	for _, r := range s.Rules {
		if _, ok := pageIDs[r.OnPage]; !ok {
			add(IssueDanglingRef, SeverityError, string(r.ID), "rule %q runs on page %q, which is not defined", r.ID, r.OnPage)
		}
		if r.When.Field != "" && !s.HasField(r.When.Field) {
			add(IssueDanglingRef, SeverityError, string(r.ID), "rule %q tests field %q, which is not defined", r.ID, r.When.Field)
		}
		issues = append(issues, validateCondition(s, r)...)
		for _, a := range append(slices.Clone(r.Then), r.Else...) {
			issues = append(issues, validateAction(s, r, a, pageIDs)...)
		}
	}

	// Anything below walks the navigation graph, which is only meaningful once
	// every reference resolves.
	if hasErrors(issues) {
		return ValidationResult{OK: false, Issues: issues}
	}

	// 4. Reachability and cycles.
	reachable, cycle := walkGraph(s)
	if cycle != "" {
		add(IssueCycle, SeverityError, string(cycle),
			"navigation can loop back to page %q, so a respondent could never reach the end", cycle)
	}
	for _, p := range s.Pages {
		if _, ok := reachable[p.ID]; !ok {
			add(IssueUnreachable, SeverityWarning, string(p.ID),
				"page %q cannot be reached from the first page", p.ID)
		}
	}

	// 5. Required fields that can never be shown.
	//
	//    This is the classic break: a required question sits behind a branch
	//    nobody can take, so the form silently refuses every submission. The check
	//    is conservative -- it flags fields that are unreachable on *every* path,
	//    not fields merely unlikely to appear -- because enumerating all paths is
	//    exponential and a false accusation here blocks a legitimate publish.
	for fid, f := range s.Fields {
		if !f.Required {
			continue
		}
		page, placed := fieldPage[fid]
		if !placed {
			continue // already reported as an orphan
		}
		if _, ok := reachable[page]; !ok {
			add(IssueRequiredHidden, SeverityError, string(fid),
				"field %q is required but sits on unreachable page %q", fid, page)
			continue
		}
		if f.Hidden && !everShown(s, fid) {
			add(IssueRequiredHidden, SeverityError, string(fid),
				"field %q is required and hidden, but no rule ever shows it", fid)
		}
		if alwaysHidden(s, fid) {
			add(IssueRequiredHidden, SeverityError, string(fid),
				"field %q is required but is hidden unconditionally by a rule", fid)
		}
	}

	// 6. Consent obligations. A form collecting personal data without a declared
	//    purpose has no lawful basis to stand on, so this blocks publishing
	//    rather than merely warning.
	if s.HasPIIFields() && len(s.Consent.Purposes) == 0 {
		add(IssueConsentMissing, SeverityError, "",
			"this form collects personal data but declares no processing purpose")
	}
	if s.HasSensitiveFields() && !s.Consent.SensitiveNoticeRequired {
		add(IssueSensitiveNotice, SeverityError, "",
			"this form collects sensitive personal data, so the data subject must be told so explicitly")
	}

	return ValidationResult{OK: !hasErrors(issues), Issues: issues}
}

func validateField(id FieldID, f Field) []Issue {
	var issues []Issue
	bad := func(format string, args ...any) {
		issues = append(issues, Issue{
			Code: IssueBadFieldConfig, Severity: SeverityError, Target: string(id),
			Message: fmt.Sprintf(format, args...),
		})
	}

	switch f.Type {
	case TypeText, TypeDate:
	case TypeFile:
		if f.MaxMB < 0 {
			bad("field %q has a negative size limit", id)
		}
	case TypeChoice, TypeMultiChoice, TypeDropdown:
		if len(f.Options) == 0 {
			bad("field %q is a %s but declares no options", id, f.Type)
		}
		seen := make(map[OptionID]struct{}, len(f.Options))
		for _, o := range f.Options {
			if o.ID == "" {
				bad("field %q has an option without an id", id)
				continue
			}
			if _, dup := seen[o.ID]; dup {
				bad("field %q reuses option id %q", id, o.ID)
			}
			seen[o.ID] = struct{}{}
		}
	case TypeRating:
		if f.Scale < 2 || f.Scale > 10 {
			bad("field %q is a rating with scale %d; it must be between 2 and 10", id, f.Scale)
		}
	default:
		bad("field %q has unknown type %q", id, f.Type)
	}

	if strings.TrimSpace(f.Label) == "" {
		// A question with no text is not a question. Publishing allowed it, and
		// the respondent got an input box with nothing above it -- unanswerable
		// in a way that reads as a rendering fault rather than a missing label.
		bad("field %q has no label, so nobody can tell what it is asking", id)
	}
	if f.Identifier && f.PII == "" {
		bad("field %q identifies the data subject, so it must declare what personal data it holds", id)
	}
	if f.Identifier && !f.Sensitive {
		// A warning, not an error: a phone number is basic personal data under
		// Law 91/2025 and encrypting it is not required.
		//
		// It is still worth saying. The subject table stores identifiers as HMAC
		// digests specifically so it cannot be read as a directory; a direct
		// identifier left in plaintext in the answers column -- and indexed --
		// undoes that next door. Erasure also differs: a sensitive field dies
		// with its key, a plain one only with its row.
		issues = append(issues, Issue{
			Code: IssueBadFieldConfig, Severity: SeverityWarning, Target: string(id),
			Message: fmt.Sprintf(
				"field %q identifies the data subject but is not marked sensitive, "+
					"so it is stored and indexed in the clear", id),
		})
	}
	return issues
}

func validateCondition(s Schema, r Rule) []Issue {
	var issues []Issue
	bad := func(code, format string, args ...any) {
		issues = append(issues, Issue{
			Code: code, Severity: SeverityError, Target: string(r.ID),
			Message: fmt.Sprintf(format, args...),
		})
	}

	switch r.When.Op {
	case OpEq, OpNeq, OpIn, OpNotIn, OpGt, OpGte, OpLt, OpLte, OpBetween, OpContains, OpIsEmpty, OpIsNotEmpty:
	default:
		bad(IssueBadFieldConfig, "rule %q uses unknown operator %q", r.ID, r.When.Op)
		return issues
	}

	// A rule comparing against an option that was deleted is the single most
	// common way an edit breaks a branch, so it blocks publishing outright.
	f, ok := s.Fields[r.When.Field]
	if !ok || !f.IsChoice() {
		return issues
	}
	for _, v := range conditionValues(r.When) {
		if str, isStr := v.(string); isStr && !f.HasOption(OptionID(str)) {
			bad(IssueDanglingRef, "rule %q compares field %q against option %q, which no longer exists",
				r.ID, r.When.Field, str)
		}
	}
	return issues
}

func conditionValues(c Condition) []any {
	switch t := c.Value.(type) {
	case nil:
		return nil
	case []any:
		return t
	default:
		return []any{c.Value}
	}
}

func validateAction(s Schema, r Rule, a Action, pages map[PageID]int) []Issue {
	var issues []Issue
	bad := func(format string, args ...any) {
		issues = append(issues, Issue{
			Code: IssueDanglingRef, Severity: SeverityError, Target: string(r.ID),
			Message: fmt.Sprintf(format, args...),
		})
	}

	switch a.Action {
	case ActionShow, ActionHide, ActionRequire, ActionOptional:
		if !s.HasField(FieldID(a.Target)) {
			bad("rule %q performs %s on field %q, which is not defined", r.ID, a.Action, a.Target)
			break
		}
		// The field must live on the page the rule runs on.
		//
		// evalPage builds its visibility map from that page's own field list and
		// iterates only that list, so an action aimed at a field on another page
		// changes nothing. Without this check the rule saves, publishes, reads
		// correctly in the builder, and never shows the question to anybody --
		// a failure with no error attached to it anywhere.
		if home, ok := fieldPage(s, FieldID(a.Target)); ok && home != r.OnPage {
			bad("rule %q on page %q performs %s on field %q, which is on page %q; "+
				"an action can only affect fields on the page its rule runs on",
				r.ID, r.OnPage, a.Action, a.Target, home)
		}
	case ActionGoto:
		if _, ok := pages[PageID(a.Target)]; !ok {
			bad("rule %q jumps to page %q, which is not defined", r.ID, a.Target)
		}
	case ActionEnd:
	default:
		issues = append(issues, Issue{
			Code: IssueBadFieldConfig, Severity: SeverityError, Target: string(r.ID),
			Message: fmt.Sprintf("rule %q uses unknown action %q", r.ID, a.Action),
		})
	}
	return issues
}

// walkGraph returns the reachable pages and the first page found on a cycle.
//
// Edges are the sequential fallthrough plus every goto target, because a
// respondent's answers decide which is taken and validation must assume any of
// them can be.
func walkGraph(s Schema) (reachable map[PageID]struct{}, cycle PageID) {
	edges := make(map[PageID][]PageID, len(s.Pages))
	terminal := make(map[PageID]bool, len(s.Pages))

	for i, p := range s.Pages {
		switch {
		case p.Next != "":
			edges[p.ID] = append(edges[p.ID], p.Next)
		case i+1 < len(s.Pages):
			edges[p.ID] = append(edges[p.ID], s.Pages[i+1].ID)
		}
	}
	for _, r := range s.Rules {
		for _, a := range append(slices.Clone(r.Then), r.Else...) {
			switch a.Action {
			case ActionGoto:
				edges[r.OnPage] = append(edges[r.OnPage], PageID(a.Target))
			case ActionEnd:
				terminal[r.OnPage] = true
			}
		}
	}

	reachable = make(map[PageID]struct{}, len(s.Pages))
	const (
		white = 0 // unvisited
		grey  = 1 // on the current stack
		black = 2 // fully explored
	)
	colour := make(map[PageID]int, len(s.Pages))

	var visit func(PageID) PageID
	visit = func(id PageID) PageID {
		switch colour[id] {
		case grey:
			return id // back edge: a cycle through this page
		case black:
			return ""
		}
		colour[id] = grey
		reachable[id] = struct{}{}
		for _, next := range edges[id] {
			if found := visit(next); found != "" {
				return found
			}
		}
		colour[id] = black
		return ""
	}

	cycle = visit(s.Pages[0].ID)
	return reachable, cycle
}

// everShown reports whether any rule can make a hidden field visible.
func everShown(s Schema, id FieldID) bool {
	for _, r := range s.Rules {
		for _, a := range append(slices.Clone(r.Then), r.Else...) {
			if a.Action == ActionShow && FieldID(a.Target) == id {
				return true
			}
		}
	}
	return false
}

// alwaysHidden reports whether a rule hides the field on both branches, which
// means no set of answers can reveal it.
func alwaysHidden(s Schema, id FieldID) bool {
	for _, r := range s.Rules {
		hidesInThen := hasAction(r.Then, ActionHide, string(id))
		hidesInElse := hasAction(r.Else, ActionHide, string(id))
		// Only a rule with no else branch, or one that hides on both, is
		// unconditional: a rule that hides in `then` alone leaves the `else`
		// path showing the field.
		if hidesInThen && (hidesInElse || len(r.Else) == 0) {
			// A later rule may show it again; declaration order decides.
			if !everShown(s, id) {
				return true
			}
		}
	}
	return false
}

func hasAction(actions []Action, action, target string) bool {
	for _, a := range actions {
		if a.Action == action && a.Target == target {
			return true
		}
	}
	return false
}

func hasErrors(issues []Issue) bool {
	for _, i := range issues {
		if i.Severity == SeverityError {
			return true
		}
	}
	return false
}

// fieldPage reports which page lists a field.
//
// A field belongs to exactly one page: page membership is what decides when it
// is asked, so a field on two pages would be asked twice with one answer.
func fieldPage(s Schema, id FieldID) (PageID, bool) {
	for _, p := range s.Pages {
		for _, fid := range p.Fields {
			if fid == id {
				return p.ID, true
			}
		}
	}
	return "", false
}
