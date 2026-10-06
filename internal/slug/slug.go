// Package slug turns titles into URL-safe identifiers.
package slug

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Make lowercases s, strips accents and replaces runs of non-alphanumerics with "-".
func Make(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range norm.NFD.String(strings.ToLower(s)) {
		switch {
		case unicode.Is(unicode.Mn, r), r == '\'', r == '’':
			// drop combining accents and apostrophes ("Children's" -> "childrens")
		case r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			b.WriteRune(r)
			dash = false
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	out := strings.TrimSuffix(b.String(), "-")
	if len(out) > 80 {
		out = strings.TrimSuffix(out[:80], "-")
	}
	if out == "" {
		out = "untitled"
	}
	return out
}
