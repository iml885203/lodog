package readiness

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Text is a sentence the score writes about a document. It keeps the key of
// its Chinese template in Texts and its arguments, so the report can show it
// in another language with its own dictionary; it prints and marshals as the
// Chinese sentence, which is what lodog score and its JSON give.
type Text struct {
	// Key names the template in Texts; empty for a literal.
	Key string
	// Args fill {0}, {1}… of the template.
	Args []Text
	// Lit is a piece of the document shown as it stands in every language
	// (a path, a count, a parameter name); set when Key is empty.
	Lit string
}

// say is the sentence key with args: a Text argument is a sentence of its
// own, anything else is printed as a literal.
func say(key string, args ...any) Text {
	t := Text{Key: key}
	for _, a := range args {
		if sub, ok := a.(Text); ok {
			t.Args = append(t.Args, sub)
			continue
		}
		t.Args = append(t.Args, Lit(fmt.Sprint(a)))
	}
	return t
}

// Lit is a piece of the document that is not translated.
func Lit(s string) Text { return Text{Lit: s} }

// IsZero is true for a Text that says nothing.
func (t Text) IsZero() bool { return t.Key == "" && t.Lit == "" }

// String is the sentence in Chinese. A key missing from Texts shows as the
// key itself; a test keeps Texts and the code in step.
func (t Text) String() string {
	if t.Key == "" {
		return t.Lit
	}
	s, ok := Texts[t.Key]
	if !ok {
		return t.Key
	}
	for i, a := range t.Args {
		s = strings.ReplaceAll(s, "{"+strconv.Itoa(i)+"}", a.String())
	}
	return s
}

// MarshalJSON writes the Chinese sentence, so the JSON of lodog score keeps
// plain strings.
func (t Text) MarshalJSON() ([]byte, error) { return json.Marshal(t.String()) }

// UnmarshalJSON reads a sentence back as a literal.
func (t *Text) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	*t = Lit(s)
	return nil
}

// joined chains xs with the two-place template key ("{0}、{1}"), so a list
// stays a sentence whose separator each language writes its own way.
func joined(key string, xs []Text) Text {
	switch len(xs) {
	case 0:
		return Text{}
	case 1:
		return xs[0]
	}
	return say(key, xs[0], joined(key, xs[1:]))
}

func lits(xs []string) []Text {
	out := make([]Text, len(xs))
	for i, x := range xs {
		out[i] = Lit(x)
	}
	return out
}
