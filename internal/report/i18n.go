package report

import (
	"encoding/json"
	"fmt"
	"html/template"
	"sort"
	"strings"
)

// The page speaks Traditional Chinese and English, switched in the browser.
// Every fixed sentence is a key in the dictionaries (i18n_zh.go, i18n_en.go).
// A piece of text that comes from the data (a task's wording, an agent's
// answer, an API path, an L4 instruction) is never a key: it is passed in as
// an argument or as a raw Msg and shown as it is.

// Langs are the page's languages; the first is what the HTML is written in.
var Langs = []string{"zh", "en"}

// Msg is one sentence of the page: a dictionary key with its arguments, or a
// literal (Raw) that is shown as it is in both languages.
//
// An argument that starts with "@" names a key without arguments ("@level.L1"
// is the level's name); one that starts with "#" is a comma-separated list
// joined with the language's separator.
type Msg struct {
	Key  string   `json:"k,omitempty"`
	Args []string `json:"a,omitempty"`
	Raw  string   `json:"r,omitempty"`
}

// M is the sentence key with args; anything that is not a string is printed.
func M(key string, args ...any) Msg {
	m := Msg{Key: key}
	for _, a := range args {
		m.Args = append(m.Args, fmt.Sprint(a))
	}
	return m
}

// Raw is a literal that is not translated.
func Raw(s string) Msg { return Msg{Raw: s} }

// IsZero is true for a Msg that says nothing.
func (m Msg) IsZero() bool { return m.Key == "" && m.Raw == "" }

// LevelArg names a level's own name as a Msg argument.
func LevelArg(level string) string { return "@level." + level }

// Text is the sentence in a language. A key missing from the dictionary shows
// as the key itself: a test keeps both dictionaries and the code in step.
func (m Msg) Text(lang string) string {
	if m.Key == "" {
		return m.Raw
	}
	tpl, ok := dicts[lang][m.Key]
	if !ok {
		return m.Key
	}
	return fill(lang, tpl, m.Args)
}

// Zh is the sentence in Chinese, the language the HTML is written in.
func (m Msg) Zh() string { return m.Text("zh") }

// En is the sentence in English.
func (m Msg) En() string { return m.Text("en") }

func fill(lang, tpl string, args []string) string {
	for i, a := range args {
		tpl = strings.ReplaceAll(tpl, fmt.Sprintf("{%d}", i), resolveArg(lang, a))
	}
	return tpl
}

func resolveArg(lang, a string) string {
	switch {
	case strings.HasPrefix(a, "@"):
		if t, ok := dicts[lang][a[1:]]; ok {
			return t
		}
	case strings.HasPrefix(a, "#"):
		parts := strings.Split(a[1:], ",")
		for i, p := range parts {
			parts[i] = resolveArg(lang, p)
		}
		return strings.Join(parts, dicts[lang]["sep"])
	}
	return a
}

var dicts = map[string]map[string]string{"zh": zh, "en": en}

// dictJSON is both dictionaries for the page's script.
func dictJSON() (string, error) {
	b, err := json.Marshal(dicts)
	return string(b), err
}

// Keys lists the keys of a language, sorted; the tests compare the languages.
func Keys(lang string) []string {
	out := make([]string, 0, len(dicts[lang]))
	for k := range dicts[lang] {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// spanOf is a Msg as the page shows it: Chinese text in a span that carries
// the key, so the script can switch language. `code` spans become <code>.
func spanOf(m Msg) template.HTML {
	if m.Key == "" {
		return template.HTML(template.HTMLEscapeString(m.Raw)) //nolint:gosec // G203: the text is escaped here
	}
	attrs := fmt.Sprintf(` data-k="%s"`, template.HTMLEscapeString(m.Key))
	if len(m.Args) > 0 {
		b, _ := json.Marshal(m.Args)
		attrs += fmt.Sprintf(` data-a="%s"`, template.HTMLEscapeString(string(b)))
	}
	return template.HTML(`<span class="i"` + attrs + `>` + codeSpans(m.Zh()) + `</span>`) //nolint:gosec // G203: key, args and text are escaped here
}

// tspanOf is a Msg inside an SVG <text>, where a <span> does not render.
func tspanOf(m Msg) template.HTML {
	s := string(spanOf(m))
	if m.Key == "" {
		return template.HTML(s) //nolint:gosec // G203: spanOf escaped the text
	}
	return template.HTML(strings.Replace(strings.Replace(s, "<span", "<tspan", 1), "</span>", "</tspan>", 1)) //nolint:gosec // G203: spanOf escaped every part
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

// attrOf is an attribute that names what it says by a key, in Chinese with the
// key beside it for the script: aria-label="…" data-ia-aria-label="key".
func attrOf(name string, m Msg) template.HTMLAttr {
	s := fmt.Sprintf(`%s="%s" data-ia-%s="%s"`, name, template.HTMLEscapeString(m.Zh()), name, template.HTMLEscapeString(m.Key))
	if len(m.Args) > 0 {
		b, _ := json.Marshal(m.Args)
		s += fmt.Sprintf(` data-ia-%s-a="%s"`, name, template.HTMLEscapeString(string(b)))
	}
	return template.HTMLAttr(s) //nolint:gosec // G203: name is a literal of the caller, the rest is escaped here
}
