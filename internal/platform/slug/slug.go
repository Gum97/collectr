// Package slug turns a display name into a URL-safe identifier.
package slug

import (
	"strings"
	"unicode"
)

// vietnameseFolding maps the letters Vietnamese adds to the Latin alphabet onto
// their unaccented forms.
//
// Without it, a plain a-z filter turns "Tuyển dụng" into "tuyn-dng": every
// accented vowel silently deleted. That is what a naive slugifier does to the
// language this product is built for, and it shows up in every URL the customer
// sees. đ needs its own entry because it is a distinct letter rather than a d
// carrying a mark.
var vietnameseFolding = map[rune]rune{
	'à': 'a', 'á': 'a', 'ả': 'a', 'ã': 'a', 'ạ': 'a',
	'ă': 'a', 'ằ': 'a', 'ắ': 'a', 'ẳ': 'a', 'ẵ': 'a', 'ặ': 'a',
	'â': 'a', 'ầ': 'a', 'ấ': 'a', 'ẩ': 'a', 'ẫ': 'a', 'ậ': 'a',
	'è': 'e', 'é': 'e', 'ẻ': 'e', 'ẽ': 'e', 'ẹ': 'e',
	'ê': 'e', 'ề': 'e', 'ế': 'e', 'ể': 'e', 'ễ': 'e', 'ệ': 'e',
	'ì': 'i', 'í': 'i', 'ỉ': 'i', 'ĩ': 'i', 'ị': 'i',
	'ò': 'o', 'ó': 'o', 'ỏ': 'o', 'õ': 'o', 'ọ': 'o',
	'ô': 'o', 'ồ': 'o', 'ố': 'o', 'ổ': 'o', 'ỗ': 'o', 'ộ': 'o',
	'ơ': 'o', 'ờ': 'o', 'ớ': 'o', 'ở': 'o', 'ỡ': 'o', 'ợ': 'o',
	'ù': 'u', 'ú': 'u', 'ủ': 'u', 'ũ': 'u', 'ụ': 'u',
	'ư': 'u', 'ừ': 'u', 'ứ': 'u', 'ử': 'u', 'ữ': 'u', 'ự': 'u',
	'ỳ': 'y', 'ý': 'y', 'ỷ': 'y', 'ỹ': 'y', 'ỵ': 'y',
	'đ': 'd',
}

// Make returns a lowercase, hyphenated identifier.
//
// Runs of separators collapse and edge hyphens are trimmed, so two names that
// differ only in punctuation cannot produce two slugs that look identical.
func Make(name string) string {
	var b strings.Builder
	b.Grow(len(name))

	lastHyphen := true // also suppresses a leading hyphen
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		if folded, ok := vietnameseFolding[r]; ok {
			r = folded
		}
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastHyphen = false
		case r == '-', r == '_', r == '.', unicode.IsSpace(r):
			if !lastHyphen {
				b.WriteByte('-')
				lastHyphen = true
			}
		default:
			// Other scripts, emoji and punctuation are dropped rather than
			// transliterated: a slug is an identifier, not a translation.
		}
	}

	return strings.Trim(b.String(), "-")
}
