package redact

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Limits on what a person approving, or a record, is shown of a call.
const (
	// MaxValueRunes is the longest string shown whole; a longer one is shown
	// as its size and its first MaxValueHead characters.
	MaxValueRunes = 200
	// MaxValueHead is how much of a long string is still shown.
	MaxValueHead = 40
	// MaxPreviewRunes caps the whole rendering.
	MaxPreviewRunes = 1500
)

// credentialTerms is the one list of what a credential is called, whatever the
// API. A name is compared lower-cased with "_", "-" and spaces removed, so
// access_key, access-key, accessKey and AccessKey are all "accesskey". A term
// matches inside a longer name ("userPassword", "x-auth-token").
var credentialTerms = []string{
	"password", "passwd", "passphrase", "pwd",
	"secret", "token", "jwt", "bearer",
	"apikey", "accesskey", "privatekey", "signingkey",
	"credential", "authorization",
	"session", "cookie",
}

// shortCredentialWords are too short to match inside a longer name ("pin" is
// in "spinner", "otp" in "footprint"), so a name is checked word by word for
// them: pin, userPin and user_pin match; spinner does not.
var shortCredentialWords = []string{"pass", "pin", "otp", "cvv", "cvc"}

// credentialQueryNames is credentialTerms and shortCredentialWords as they are
// spelled in a query string or a JSON field, for the name-based masking of
// redact.Names.Text.
var credentialQueryNames = func() []string {
	var out []string
	for _, t := range append(slices.Clone(credentialTerms), shortCredentialWords...) {
		out = append(out, t)
		switch t {
		case "apikey":
			out = append(out, "api_key", "api-key")
		case "accesskey":
			out = append(out, "access_key", "access-key")
		case "privatekey":
			out = append(out, "private_key", "private-key")
		case "signingkey":
			out = append(out, "signing_key", "signing-key")
		}
	}
	return out
}()

var credentialText = Names{Queries: credentialQueryNames, Inside: true}

// IsCredentialName reports whether a field or parameter name carries a
// credential.
func IsCredentialName(name string) bool {
	words := splitWords(name)
	for _, w := range words {
		if slices.Contains(shortCredentialWords, w) {
			return true
		}
	}
	joined := strings.Join(words, "")
	return slices.ContainsFunc(credentialTerms, func(t string) bool { return strings.Contains(joined, t) })
}

// splitWords lower-cases name and splits it at "_", "-", spaces, dots and a
// lower-to-upper camelCase boundary.
func splitWords(name string) []string {
	var words []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			words = append(words, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	prev := rune(0)
	for _, r := range name {
		switch {
		case r == '_' || r == '-' || r == ' ' || r == '.':
			flush()
		case unicode.IsUpper(r) && unicode.IsLower(prev):
			flush()
			cur = append(cur, r)
		default:
			cur = append(cur, r)
		}
		prev = r
	}
	flush()
	return words
}

// MaskString masks the credentials a string carries: a password in URL user
// info and the value of a credential-named query parameter or key=value pair,
// by the same rules redact.Names.Text applies to an error message.
func MaskString(s string) string { return credentialText.Text(s) }

// Preview returns v as a person may be shown it: the value of a field whose
// name looks like a credential is masked, and a long string is replaced by its
// size and first characters, so a pasted file or a base64 blob cannot flood
// the screen.
func Preview(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			if IsCredentialName(k) {
				out[k] = Mask
				continue
			}
			out[k] = Preview(val)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = Preview(val)
		}
		return out
	case string:
		x = MaskString(x)
		if n := utf8.RuneCountInString(x); n > MaxValueRunes {
			return fmt.Sprintf("%s…（%d 字元，已截斷）", string([]rune(x)[:MaxValueHead]), n)
		}
		return x
	}
	return v
}

// PreviewJSON renders args through Preview, capped at MaxPreviewRunes. indent
// selects the multi-line form a page shows.
func PreviewJSON(args map[string]any, indent bool) string {
	var (
		b   []byte
		err error
	)
	if indent {
		b, err = json.MarshalIndent(Preview(args), "", "  ")
	} else {
		b, err = json.Marshal(Preview(args))
	}
	if err != nil {
		return "(arguments could not be shown)"
	}
	s := string(b)
	if r := []rune(s); len(r) > MaxPreviewRunes {
		s = string(r[:MaxPreviewRunes]) + "\n…（太長，已截斷）"
	}
	return s
}

// FieldNames lists the top-level field names of a body, sorted, never their
// values: what a summary may say about a body.
func FieldNames(body map[string]any) []string {
	names := make([]string, 0, len(body))
	for k := range body {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// FieldList is FieldNames as one phrase, empty when there is no body.
func FieldList(body map[string]any) string {
	if len(body) == 0 {
		return ""
	}
	return "body fields: " + strings.Join(FieldNames(body), ", ")
}

// JSONLines masks the credentials in newline-delimited JSON, such as an
// agent's stream-json transcript: every string value anywhere in a line goes
// through MaskString, so a tool result that carries a reset link or a token
// is masked whatever it is nested in. A line that is not JSON is masked as
// text. The lines and their order are kept; a JSON object comes back with its keys sorted.
func JSONLines(raw []byte) []byte {
	var out bytes.Buffer
	for _, line := range bytes.SplitAfter(raw, []byte("\n")) {
		body := bytes.TrimRight(line, "\r\n")
		tail := line[len(body):]
		if len(bytes.TrimSpace(body)) == 0 {
			out.Write(line)
			continue
		}
		var v any
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.UseNumber()
		if err := dec.Decode(&v); err != nil {
			out.WriteString(MaskString(string(body)))
			out.Write(tail)
			continue
		}
		var enc bytes.Buffer
		e := json.NewEncoder(&enc)
		e.SetEscapeHTML(false)
		if err := e.Encode(maskStrings(v)); err != nil {
			out.WriteString(MaskString(string(body)))
			out.Write(tail)
			continue
		}
		out.Write(bytes.TrimRight(enc.Bytes(), "\n"))
		out.Write(tail)
	}
	return out.Bytes()
}

func maskStrings(v any) any {
	switch x := v.(type) {
	case string:
		return MaskString(x)
	case []any:
		for i := range x {
			x[i] = maskStrings(x[i])
		}
	case map[string]any:
		for k, e := range x {
			x[k] = maskStrings(e)
		}
	}
	return v
}
