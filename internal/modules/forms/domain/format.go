package domain

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Named formats a text field can be held to.
//
// A closed list rather than a tenant-supplied regular expression, for the same
// reason the condition operators are a closed list: a pattern box is an input
// language, and this one would be evaluated in every respondent's browser.
// Go's regexp is RE2 and runs in linear time, but the client's is not -- a
// pattern that backtracks catastrophically would freeze the phone of the person
// filling in the form, and the tenant admin who pasted it would never see it
// happen.
//
// The list is also what makes a Vietnamese error message possible. "Không khớp
// mẫu ^[0-9]{10}$" tells a respondent nothing; "Mã số thuế gồm 10 chữ số" tells
// them what to type.
const (
	FormatEmail      = "email"
	FormatPhoneVN    = "phone_vn"
	FormatTaxCode    = "tax_code"
	FormatNationalID = "national_id"
	FormatURL        = "url"
	FormatNumber     = "number"
	FormatInteger    = "integer"
)

// formatSpec describes one named format.
type formatSpec struct {
	// Label names the format in the builder.
	Label string
	// Hint is shown to a respondent who gets it wrong. It says what to type,
	// never what the pattern is.
	Hint string
	// InputMode is the keyboard a phone should offer. Getting this wrong makes a
	// respondent hunt for the digit keys on a 12-digit id.
	InputMode string
	// Numeric marks the formats that Min and Max compare as numbers rather than
	// as strings.
	Numeric bool

	pattern *regexp.Regexp
	// strip is removed before matching. People type phone numbers with spaces
	// and tax codes with dots; rejecting that is rejecting a correct answer for
	// being punctuated.
	strip string
}

// formats is the whitelist. Deliberately permissive where the real-world data is
// messier than the specification: a validator that refuses a valid answer costs
// a submission, and there is no way to tell afterwards that it happened.
var formats = map[string]formatSpec{
	FormatEmail: {
		Label:     "Email",
		Hint:      "Địa chỉ email chưa đúng dạng, ví dụ: ten@congty.vn",
		InputMode: "email",
		// One @, something either side, a dot in the domain. Not RFC 5322:
		// that grammar accepts addresses no mail server would route, and
		// rejecting a real address is the expensive mistake here.
		pattern: regexp.MustCompile(`^[^@\s]+@[^@\s.]+(\.[^@\s.]+)+$`),
	},
	FormatPhoneVN: {
		Label:     "Số điện thoại Việt Nam",
		Hint:      "Số điện thoại gồm 10 chữ số bắt đầu bằng 0, ví dụ: 0912345678",
		InputMode: "tel",
		strip:     " .-()",
		// 0xxxxxxxxx, or the same number written +84xxxxxxxxx. Landlines and
		// mobiles are both 10 digits after the leading zero was standardised.
		pattern: regexp.MustCompile(`^(?:0|\+84)\d{9}$`),
	},
	FormatTaxCode: {
		Label:     "Mã số thuế",
		Hint:      "Mã số thuế gồm 10 chữ số, hoặc 10 chữ số kèm 3 số đơn vị phụ thuộc (ví dụ 0123456789-001)",
		InputMode: "numeric",
		strip:     " .",
		pattern:   regexp.MustCompile(`^\d{10}(-\d{3})?$`),
	},
	FormatNationalID: {
		Label:     "Số CCCD",
		Hint:      "Số căn cước công dân gồm 12 chữ số",
		InputMode: "numeric",
		strip:     " .",
		// 12 digits only. The 9-digit CMND was retired, and accepting it here
		// would let stale identifiers into a field used to recognise a data
		// subject across submissions.
		pattern: regexp.MustCompile(`^\d{12}$`),
	},
	FormatURL: {
		Label:     "Đường dẫn",
		Hint:      "Đường dẫn phải bắt đầu bằng http:// hoặc https://",
		InputMode: "url",
		pattern:   regexp.MustCompile(`^https?://[^\s/]+\.[^\s/]+`),
	},
	FormatNumber: {
		Label:     "Số",
		Hint:      "Chỉ nhập số",
		InputMode: "decimal",
		Numeric:   true,
		strip:     " ",
		pattern:   regexp.MustCompile(`^-?\d+([.,]\d+)?$`),
	},
	FormatInteger: {
		Label:     "Số nguyên",
		Hint:      "Chỉ nhập số nguyên",
		InputMode: "numeric",
		Numeric:   true,
		strip:     " ",
		pattern:   regexp.MustCompile(`^-?\d+$`),
	},
}

// KnownFormat reports whether name is a supported format.
func KnownFormat(name string) bool {
	_, ok := formats[name]
	return ok
}

// FormatNumeric reports whether a format's Min and Max are numbers.
func FormatNumeric(name string) bool {
	return formats[name].Numeric
}

// FormatInputMode returns the keyboard hint for a format, or "".
func FormatInputMode(name string) string {
	return formats[name].InputMode
}

// FormatNames lists the supported formats in a stable order.
func FormatNames() []string {
	return []string{
		FormatEmail, FormatPhoneVN, FormatTaxCode,
		FormatNationalID, FormatURL, FormatNumber, FormatInteger,
	}
}

// CheckFormat validates one answer against a field's format and bounds.
//
// Returns a message for the respondent, or "" when the value passes. An empty
// value always passes: whether a blank answer is acceptable is required-ness,
// which is decided by the rule engine and not here. Deciding it twice is how a
// field ends up unanswerable -- hidden by a rule, yet rejected for being empty.
func CheckFormat(f Field, raw string) string {
	v := strings.TrimSpace(raw)
	if v == "" {
		return ""
	}

	if f.Format != "" {
		spec, ok := formats[f.Format]
		if !ok {
			// An unknown format reaching a live submission means a schema was
			// published against a newer build than the one now serving it.
			// Refusing the answer would lose it; the bounds below still apply.
			return ""
		}
		if !spec.pattern.MatchString(stripChars(v, spec.strip)) {
			return spec.Hint
		}
		if spec.Numeric {
			return checkNumericBounds(f, stripChars(v, spec.strip))
		}
		return ""
	}

	if f.Type == TypeDate {
		return checkDateBounds(f, v)
	}
	return ""
}

// checkNumericBounds applies Min and Max to a number.
func checkNumericBounds(f Field, v string) string {
	if f.Min == "" && f.Max == "" {
		return ""
	}
	// Commas are the Vietnamese decimal separator; the pattern already allowed
	// one, so the parse has to as well.
	n, err := strconv.ParseFloat(strings.Replace(v, ",", ".", 1), 64)
	if err != nil {
		return ""
	}
	if f.Min != "" {
		if lo, err := strconv.ParseFloat(f.Min, 64); err == nil && n < lo {
			return fmt.Sprintf("Giá trị phải từ %s trở lên", f.Min)
		}
	}
	if f.Max != "" {
		if hi, err := strconv.ParseFloat(f.Max, 64); err == nil && n > hi {
			return fmt.Sprintf("Giá trị không được vượt quá %s", f.Max)
		}
	}
	return ""
}

// checkDateBounds applies Min and Max to a date, both in YYYY-MM-DD form.
//
// Compared as strings on purpose: ISO dates sort lexicographically, the values
// are already known to parse, and a string compare cannot pick up a timezone.
func checkDateBounds(f Field, v string) string {
	if f.Min != "" && v < f.Min {
		return fmt.Sprintf("Ngày phải từ %s trở đi", f.Min)
	}
	if f.Max != "" && v > f.Max {
		return fmt.Sprintf("Ngày không được sau %s", f.Max)
	}
	return ""
}

func stripChars(s, cutset string) string {
	if cutset == "" {
		return s
	}
	return strings.Map(func(r rune) rune {
		if strings.ContainsRune(cutset, r) {
			return -1
		}
		return r
	}, s)
}
