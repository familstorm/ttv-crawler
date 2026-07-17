package parser

import (
	"html"
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

var whitespaceRE = regexp.MustCompile(`\s+`)

func Text(value string) string {
	value = html.UnescapeString(value)
	value = norm.NFC.String(value)
	return strings.TrimSpace(whitespaceRE.ReplaceAllString(value, " "))
}

func Key(value string) string {
	value = strings.ToLower(Text(value))
	decomposed := norm.NFD.String(value)
	var b strings.Builder
	for _, r := range decomposed {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		if r == 'đ' {
			r = 'd'
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			continue
		}
		if b.Len() > 0 && !strings.HasSuffix(b.String(), "-") {
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}
