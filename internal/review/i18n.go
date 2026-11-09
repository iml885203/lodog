package review

import (
	"encoding/json"
	"fmt"
	"html/template"
	"strings"
)

// The page speaks Traditional Chinese and English, switched in the browser,
// the same way the report page does (internal/report/i18n.go). Every sentence
// lodog writes itself is a key in the two dictionaries (i18n_zh.go,
// i18n_en.go); text that comes from the data (an operation key, the model's
// summary, the checklist's own ask and why) is shown as it is.
//
// An argument that starts with "@" names a key without arguments.

var dicts = map[string]map[string]string{"zh": zh, "en": en}

// text is the sentence of a key in a language; a missing key shows as the key.
func text(lang, key string, args []string) string {
	tpl, ok := dicts[lang][key]
	if !ok {
		return key
	}
	for i, a := range args {
		if strings.HasPrefix(a, "@") {
			if t, ok := dicts[lang][a[1:]]; ok {
				a = t
			}
		}
		tpl = strings.ReplaceAll(tpl, fmt.Sprintf("{%d}", i), a)
	}
	return tpl
}

func dictJSON() (template.JS, error) {
	b, err := json.Marshal(dicts)
	return template.JS(b), err //nolint:gosec // G203: encoding/json escapes the markup characters
}

// span is a sentence as the page shows it: Chinese text in a span carrying the
// key and arguments, so the script can switch language.
func span(key string, args ...string) template.HTML {
	attrs := fmt.Sprintf(` data-k="%s"`, template.HTMLEscapeString(key))
	if len(args) > 0 {
		b, _ := json.Marshal(args)
		attrs += fmt.Sprintf(` data-a="%s"`, template.HTMLEscapeString(string(b)))
	}
	return template.HTML(`<span class="i"` + attrs + `>` + codeSpans(text("zh", key, args)) + `</span>`) //nolint:gosec // G203: key, args and text are escaped here
}

// codeSpans escapes text and turns `x` into <code>x</code>.
func codeSpans(s string) string {
	parts := strings.Split(s, "`")
	var b strings.Builder
	for i, p := range parts {
		switch {
		case i%2 == 1 && i < len(parts)-1:
			b.WriteString("<code>" + template.HTMLEscapeString(p) + "</code>")
		case i%2 == 1:
			b.WriteString("`" + template.HTMLEscapeString(p))
		default:
			b.WriteString(template.HTMLEscapeString(p))
		}
	}
	return b.String()
}
