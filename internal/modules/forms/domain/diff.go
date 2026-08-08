package domain

import (
	"fmt"
	"slices"
)

// Change classifications.
const (
	ChangeNonBreaking = "non_breaking" // old submissions render exactly as before
	ChangeBreaking    = "breaking"     // old submissions stay valid, but the grid must show them differently
	ChangeBlocked     = "blocked"      // publishing is refused
)

// Change is one difference between two versions.
type Change struct {
	Kind    string `json:"kind"`
	Class   string `json:"class"`
	Target  string `json:"target"`
	Message string `json:"message"`
}

// DiffResult summarises what publishing a draft would change.
type DiffResult struct {
	Changes  []Change `json:"changes"`
	Blocked  bool     `json:"blocked"`
	Breaking bool     `json:"breaking"`
}

// Diff compares a published version against a draft.
//
// No change ever rewrites stored answers. Old submissions were collected under a
// specific consent document and a specific set of questions; rewriting them would
// both destroy that evidence and create a class of bug with no end. What a
// breaking change alters is how the grid *presents* the old data -- see
// docs/06-deep-dives.md.
func Diff(old, draft Schema) DiffResult {
	var changes []Change
	add := func(kind, class, target, format string, args ...any) {
		changes = append(changes, Change{
			Kind: kind, Class: class, Target: target,
			Message: fmt.Sprintf(format, args...),
		})
	}

	for fid, o := range old.Fields {
		n, kept := draft.Fields[fid]
		if !kept {
			// Deliberately not blocked: retiring a question is legitimate. The
			// answers stay, and the grid marks the column as removed.
			add("field_removed", ChangeBreaking, string(fid),
				"field %q is removed; existing answers are kept and its column is marked as retired", fid)
			continue
		}
		if o.Type != n.Type {
			// No coercion. "yes" as free text and an option id that renders as
			// "Yes" are different data, and merging them into one column would
			// quietly invent facts.
			add("field_type_changed", ChangeBreaking, string(fid),
				"field %q changes type from %s to %s; the grid will show the old and new answers as separate columns",
				fid, o.Type, n.Type)
		}
		if o.Label != n.Label {
			add("label_changed", ChangeNonBreaking, string(fid),
				"field %q is relabelled from %q to %q", fid, o.Label, n.Label)
		}
		if !o.Required && n.Required {
			add("field_now_required", ChangeBreaking, string(fid),
				"field %q becomes required; submissions collected before this stay valid", fid)
		}
		if !o.Sensitive && n.Sensitive {
			add("field_now_sensitive", ChangeBreaking, string(fid),
				"field %q is now sensitive; answers collected earlier were stored unencrypted and are not retroactively protected", fid)
		}
		changes = append(changes, diffOptions(fid, o, n)...)
	}

	for fid, n := range draft.Fields {
		if _, existed := old.Fields[fid]; existed {
			continue
		}
		class := ChangeNonBreaking
		if n.Required {
			class = ChangeBreaking
		}
		add("field_added", class, string(fid),
			"field %q is added; earlier submissions show an empty cell meaning the question was never asked", fid)
	}

	if old.HasSensitiveFields() != draft.HasSensitiveFields() && draft.HasSensitiveFields() {
		add("sensitive_introduced", ChangeBreaking, "",
			"this version starts collecting sensitive data, so the consent text must be updated and republished")
	}

	res := DiffResult{Changes: changes}
	for _, c := range changes {
		switch c.Class {
		case ChangeBlocked:
			res.Blocked = true
			res.Breaking = true
		case ChangeBreaking:
			res.Breaking = true
		}
	}
	return res
}

// diffOptions compares the option lists of one field across versions.
func diffOptions(fid FieldID, old, draft Field) []Change {
	if !old.IsChoice() || !draft.IsChoice() {
		return nil
	}
	var changes []Change
	for _, o := range old.Options {
		if draft.HasOption(o.ID) {
			continue
		}
		// Whether removing an option is merely breaking or outright blocked
		// depends on whether a rule still points at it; Validate on the draft
		// catches the dangling reference. Here it is reported as breaking so the
		// publisher sees the consequence for stored answers.
		changes = append(changes, Change{
			Kind: "option_removed", Class: ChangeBreaking, Target: string(fid),
			Message: fmt.Sprintf("option %q is removed from field %q; submissions that chose it keep the stored id and the grid shows it as retired", o.ID, fid),
		})
	}
	for _, n := range draft.Options {
		if !old.HasOption(n.ID) {
			changes = append(changes, Change{
				Kind: "option_added", Class: ChangeNonBreaking, Target: string(fid),
				Message: fmt.Sprintf("option %q is added to field %q", n.ID, fid),
			})
		}
	}
	return changes
}

// Column is one column of the submission grid.
type Column struct {
	FieldID   FieldID `json:"field_id"`
	Label     string  `json:"label"`
	Type      string  `json:"type"`
	Sensitive bool    `json:"sensitive"`
	// InVersions lists the version numbers that asked this question, which is what
	// lets the grid tell "never asked" apart from "asked and left blank".
	InVersions []int `json:"in_versions"`
	// RetiredAfter is the last version containing the field, or 0 if still live.
	RetiredAfter int `json:"retired_after,omitempty"`
	// TypeVariant distinguishes columns split by a type change.
	TypeVariant string `json:"type_variant,omitempty"`
}

// VersionedSchema pairs a schema with its version number.
type VersionedSchema struct {
	VersionNo int
	Schema    Schema
}

// BuildColumnRegistry merges every version of a form into one column list.
//
// Order follows first appearance, labels come from the newest version that has
// the field, and a field whose type changed is split into one column per type.
// Coercing the old values into the new type instead would be a silent rewrite of
// collected data.
func BuildColumnRegistry(versions []VersionedSchema) []Column {
	ordered := slices.Clone(versions)
	slices.SortFunc(ordered, func(a, b VersionedSchema) int { return a.VersionNo - b.VersionNo })

	type key struct {
		field FieldID
		typ   string
	}
	var (
		order   []key
		columns = make(map[key]*Column)
		latest  = make(map[FieldID]int)
	)

	for _, v := range ordered {
		for _, p := range v.Schema.Pages {
			for _, fid := range p.Fields {
				f, ok := v.Schema.Fields[fid]
				if !ok {
					continue
				}
				k := key{field: fid, typ: f.Type}
				col, exists := columns[k]
				if !exists {
					col = &Column{FieldID: fid, Type: f.Type}
					columns[k] = col
					order = append(order, k)
				}
				col.Label = f.Label
				col.Sensitive = col.Sensitive || f.Sensitive
				col.InVersions = append(col.InVersions, v.VersionNo)
				latest[fid] = v.VersionNo
			}
		}
	}

	newest := 0
	if n := len(ordered); n > 0 {
		newest = ordered[n-1].VersionNo
	}

	// A field appearing under more than one type gets its type appended to the
	// header, so a reader can see at a glance why there are two columns.
	typeCount := make(map[FieldID]int, len(columns))
	for k := range columns {
		typeCount[k.field]++
	}

	out := make([]Column, 0, len(order))
	for _, k := range order {
		col := columns[k]
		if typeCount[k.field] > 1 {
			col.TypeVariant = k.typ
		}
		if last := col.InVersions[len(col.InVersions)-1]; last < newest {
			col.RetiredAfter = last
		}
		out = append(out, *col)
	}
	return out
}

// Cell states in the submission grid.
const (
	// CellNotAsked -- the version this submission used did not contain the field.
	CellNotAsked = "not_asked"
	// CellHidden -- the field existed but a branch hid it from this respondent.
	CellHidden = "hidden"
	// CellBlank -- the respondent saw the question and left it empty.
	CellBlank = "blank"
	// CellAnswered -- there is a value.
	CellAnswered = "answered"
)

// CellState classifies one grid cell.
//
// Three distinct empties, not one. Collapsing them makes every statistic about
// completion rates meaningless: a question behind a branch would look abandoned
// rather than never offered.
func CellState(inSchema bool, visible []string, answers map[string]any, field FieldID) string {
	if !inSchema {
		return CellNotAsked
	}
	if !slices.Contains(visible, string(field)) {
		return CellHidden
	}
	v, ok := answers[string(field)]
	if !ok || isBlank(v) {
		return CellBlank
	}
	return CellAnswered
}

func isBlank(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return t == ""
	case []any:
		return len(t) == 0
	default:
		return false
	}
}
