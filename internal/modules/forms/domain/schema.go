// Package domain holds the form schema, the rules for changing it safely, and
// the checks that run before a version is published.
//
// Three invariants hold this module together, and everything else follows from
// them:
//
//  1. FieldID and OptionID are stable and never reused. Renaming a label does not
//     mint a new id.
//  2. Answers are stored by id, never by label. Rewording an option cannot
//     corrupt data collected under the old wording.
//  3. A published version is immutable. Editing a form creates a new version.
//
// Together they mean old submissions never need migrating: what changes is how
// they are *displayed*, not what they contain. Migrating collected data would
// also break the evidence chain, since each submission was gathered under one
// specific consent document.
package domain

import (
	"errors"
	"fmt"
)

// Identifier types. They are distinct types rather than bare strings so the
// compiler catches a page id passed where a field id belongs.
type (
	FieldID  string
	PageID   string
	OptionID string
	RuleID   string
)

// Field types supported by the builder.
const (
	TypeText        = "text"
	TypeChoice      = "choice"
	TypeMultiChoice = "multi_choice"
	TypeRating      = "rating"
	TypeDate        = "date"
	TypeDropdown    = "dropdown"
	TypeFile        = "file"
)

// Condition operators. A closed whitelist, not an expression language: an
// arbitrary expression evaluator would turn the rule builder into a way to run
// code inside the server.
const (
	OpEq         = "eq"
	OpNeq        = "neq"
	OpIn         = "in"
	OpNotIn      = "not_in"
	OpGt         = "gt"
	OpGte        = "gte"
	OpLt         = "lt"
	OpLte        = "lte"
	OpBetween    = "between"
	OpContains   = "contains"
	OpIsEmpty    = "is_empty"
	OpIsNotEmpty = "is_not_empty"
)

// Rule actions.
const (
	ActionShow     = "show"
	ActionHide     = "hide"
	ActionRequire  = "require"
	ActionOptional = "optional"
	ActionGoto     = "goto"
	ActionEnd      = "end"
)

// Default limits. They exist so evaluation cost has an upper bound: without
// them a hostile or careless schema turns every submission into a long walk.
const (
	MaxFields = 200
	MaxRules  = 300
)

// Schema is one immutable version of a form.
type Schema struct {
	V       int               `json:"v"`
	Pages   []Page            `json:"pages"`
	Fields  map[FieldID]Field `json:"fields"`
	Rules   []Rule            `json:"rules"`
	Consent ConsentBlock      `json:"consent"`
	Limits  Limits            `json:"limits"`
}

// Page groups fields and is the unit of navigation.
type Page struct {
	ID     PageID    `json:"id"`
	Title  string    `json:"title"`
	Fields []FieldID `json:"fields"`
	// Next is where the form goes after this page when no rule decides
	// otherwise. Empty means "the next page in declaration order".
	//
	// Without it, a branching form falls through to whatever page happens to be
	// declared next -- so a respondent sent down the "yes" branch can land in the
	// middle of the "no" branch. Declaration order is a layout concern and makes a
	// poor default for navigation, which is why both Google Forms and MS Forms
	// expose this as a property of the section rather than as a rule.
	Next PageID `json:"next,omitempty"`
}

// Field is one question.
type Field struct {
	Type     string `json:"type"`
	Label    string `json:"label"`
	Required bool   `json:"required,omitempty"`
	// Hidden marks a field that starts invisible and appears only when a rule
	// shows it.
	Hidden bool `json:"hidden,omitempty"`

	Options []Option `json:"options,omitempty"`

	// PII names the kind of personal data this field collects; Sensitive marks
	// data that needs encryption at rest and masking in the grid; Identifier
	// marks the field used to recognise a data subject across submissions.
	PII        string `json:"pii,omitempty"`
	Sensitive  bool   `json:"sensitive,omitempty"`
	Identifier bool   `json:"identifier,omitempty"`

	Scale     int      `json:"scale,omitempty"`
	Accept    []string `json:"accept,omitempty"`
	MaxMB     int      `json:"max_mb,omitempty"`
	Multiline bool     `json:"multiline,omitempty"`
	Min       string   `json:"min,omitempty"`
	Max       string   `json:"max,omitempty"`
}

// Option is one choice within a choice, multi_choice or dropdown field.
type Option struct {
	ID    OptionID `json:"id"`
	Label string   `json:"label"`
}

// Rule is one piece of conditional logic, scoped to the page it runs on.
type Rule struct {
	ID     RuleID    `json:"id"`
	OnPage PageID    `json:"on_page"`
	When   Condition `json:"when"`
	Then   []Action  `json:"then,omitempty"`
	Else   []Action  `json:"else,omitempty"`
}

// Condition is a single comparison against one field's answer.
type Condition struct {
	Op    string  `json:"op"`
	Field FieldID `json:"field"`
	Value any     `json:"value,omitempty"`
}

// Action is what a rule does when its condition resolves.
type Action struct {
	Action string `json:"action"`
	Target string `json:"target"` // a FieldID for show/hide/require/optional, a PageID for goto
}

// ConsentBlock declares the lawful bases this form collects under.
type ConsentBlock struct {
	Purposes                []Purpose `json:"purposes"`
	SensitiveNoticeRequired bool      `json:"sensitive_notice_required,omitempty"`
}

// Purpose is one declared processing purpose.
type Purpose struct {
	Code     string `json:"code"`
	Required bool   `json:"required,omitempty"`
}

// Limits caps the size of a schema.
type Limits struct {
	MaxFields int `json:"max_fields,omitempty"`
	MaxRules  int `json:"max_rules,omitempty"`
}

// Errors returned by form operations.
var (
	ErrFormNotFound    = errors.New("form not found")
	ErrVersionNotFound = errors.New("form version not found")
	ErrVersionRetired  = errors.New("form version retired")
	ErrNoDraft         = errors.New("form has no draft to publish")
)

// Page returns the page with the given id.
func (s Schema) Page(id PageID) (Page, bool) {
	for _, p := range s.Pages {
		if p.ID == id {
			return p, true
		}
	}
	return Page{}, false
}

// HasField reports whether the schema declares fieldID.
func (s Schema) HasField(id FieldID) bool {
	_, ok := s.Fields[id]
	return ok
}

// HasOption reports whether the field declares optionID.
func (f Field) HasOption(id OptionID) bool {
	for _, o := range f.Options {
		if o.ID == id {
			return true
		}
	}
	return false
}

// IsChoice reports whether the field carries a fixed option list.
func (f Field) IsChoice() bool {
	switch f.Type {
	case TypeChoice, TypeMultiChoice, TypeDropdown:
		return true
	default:
		return false
	}
}

// IdentifierField returns the field designated as the data subject identifier.
//
// It is what links submissions to a person for access, rectification and erasure
// requests. A form without one still works, but its submissions cannot be found
// by the person who made them.
func (s Schema) IdentifierField() (FieldID, Field, bool) {
	// Iterate pages, not the map, so the answer does not depend on Go's
	// randomised map order.
	for _, p := range s.Pages {
		for _, fid := range p.Fields {
			if f, ok := s.Fields[fid]; ok && f.Identifier {
				return fid, f, true
			}
		}
	}
	return "", Field{}, false
}

// HasSensitiveFields reports whether any field is marked sensitive.
func (s Schema) HasSensitiveFields() bool {
	for _, f := range s.Fields {
		if f.Sensitive {
			return true
		}
	}
	return false
}

// HasPIIFields reports whether any field collects personal data.
func (s Schema) HasPIIFields() bool {
	for _, f := range s.Fields {
		if f.PII != "" || f.Sensitive || f.Identifier {
			return true
		}
	}
	return false
}

// EffectiveLimits returns the schema's limits with defaults filled in.
func (s Schema) EffectiveLimits() Limits {
	l := s.Limits
	if l.MaxFields <= 0 {
		l.MaxFields = MaxFields
	}
	if l.MaxRules <= 0 {
		l.MaxRules = MaxRules
	}
	return l
}

// String renders an id pair for error messages.
func (a Action) String() string {
	return fmt.Sprintf("%s(%s)", a.Action, a.Target)
}
