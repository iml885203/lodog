// Package redact is the one place that decides what a credential looks like
// when it must not be written down: in a log line, an error handed to an
// agent, or a call record. The gateway and the recording proxy both use it so
// they cannot disagree.
package redact

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// Mask replaces every redacted value.
const Mask = "REDACTED"

// HeaderQuery is how the gateway tells the recording proxy which query
// parameter names carry a credential (comma-separated). The proxy cannot see
// the gateway's auth config, and a name list sent with each call stays correct
// when the config changes.
const HeaderQuery = "X-Lodog-Redact-Query"

// fixedHeaders carry identity whatever the auth scheme.
var fixedHeaders = []string{"Authorization", "Proxy-Authorization", "Cookie", "Set-Cookie"}

// Names is what to hide: the query parameters and headers one auth scheme
// sets itself, and the secret values the gateway holds right now. The fixed
// identity headers are always hidden on top of them.
//
// Values are matched literally (raw, URL-encoded and JSON-escaped), so on a
// 2xx business payload only the secret itself matches, provided it is at least
// MinDataValueLen long. Names are matched by shape and may over-mask a plain word
// such as "token" in an error page; that is accepted, which is why they are
// applied to errors and 4xx/5xx bodies only.
type Names struct {
	Queries []string
	Headers []string
	Values  []string
	// Inside also masks a query name that ends a longer identifier: with
	// "password" in Queries, resetPassword=..., user_password: ... and
	// x-auth-token=... are masked too, not only the bare word. It over-masks
	// a plain word that merely ends in one, which is accepted.
	Inside bool
}

// Query returns q with the value of every named parameter masked.
func Query(q url.Values, names []string) url.Values {
	out := make(url.Values, len(q))
	for k, v := range q {
		if slices.ContainsFunc(names, func(n string) bool { return strings.EqualFold(n, k) }) {
			out[k] = []string{Mask}
			continue
		}
		out[k] = slices.Clone(v)
	}
	return out
}

var userinfo = regexp.MustCompile(`(//[^/\s:@"]*:)[^/\s@"]*@`)

// MinDataValueLen is the shortest secret masked by value in text that may be
// real data. A shorter secret ("1234", "a") would match ordinary content and
// ruin a successful response; it is still masked in errors and error pages,
// where a mangled word costs less than a leak.
const MinDataValueLen = 8

// literal masks every held secret value of at least minLen characters in s.
func (n Names) literal(s string, minLen int) string {
	for _, v := range n.Values {
		if v == "" || len(v) < minLen {
			continue
		}
		escaped, _ := json.Marshal(v)
		jsonForm := strings.Trim(string(escaped), `"`)
		forms := []string{v, url.QueryEscape(v), url.PathEscape(v), jsonForm, strings.ReplaceAll(jsonForm, "/", `\/`)}
		for _, form := range forms {
			s = strings.ReplaceAll(s, form, Mask)
		}
	}
	return s
}

// Short reports how many held secrets are too short to be masked by value in
// real data (see MinDataValueLen).
func (n Names) Short() int {
	c := 0
	for _, v := range n.Values {
		if v != "" && len(v) < MinDataValueLen {
			c++
		}
	}
	return c
}

// Value masks the held secret values that are long enough to be told apart
// from data. Use it on text that may be real data: a successful response.
func (n Names) Value(s string) string { return n.literal(s, MinDataValueLen) }

// nameShape matches name however it is spelled in a URL or a JSON document:
// any non-alphanumeric character may be percent-encoded.
func nameShape(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r == ' ':
			b.WriteString(`(?:\+|%20| )`)
		default:
			fmt.Fprintf(&b, `(?:%s|%%%02X)`, regexp.QuoteMeta(string(r)), r)
		}
	}
	return b.String()
}

// Text masks credentials in free text such as an error message or an error
// page: the held values first, then by name: owned query values (name=value,
// "name":"value", fragments), the value of an owned or fixed header, and a
// password in URL user info.
func (n Names) Text(s string) string {
	s = n.literal(s, 0)
	for _, name := range n.Queries {
		before := `(?:^|[^A-Za-z0-9])`
		if n.Inside {
			before += `[A-Za-z0-9_-]*?`
		}
		re := regexp.MustCompile(`(?i)((?:` + before + `)` + nameShape(name) + `["']?\s*(?:=|%3D|:)\s*["']?)[^&\s"'#,}]*`)
		s = re.ReplaceAllString(s, "${1}"+Mask)
	}
	for _, name := range slices.Compact(slices.SortedFunc(slices.Values(slices.Concat(fixedHeaders, n.Headers)), func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })) {
		re := regexp.MustCompile(`(?i)(\b` + nameShape(name) + `["']?\s*[:=]\s*)(?:"[^"]*"|[^\r\n]*)`)
		s = re.ReplaceAllString(s, "${1}"+Mask)
	}
	return userinfo.ReplaceAllString(s, "${1}"+Mask+"@")
}

// ParseHeader reads the names the gateway sent in HeaderQuery.
func ParseHeader(v string) []string {
	var out []string
	for _, name := range strings.Split(v, ",") {
		if name = strings.TrimSpace(name); name != "" {
			out = append(out, name)
		}
	}
	return out
}

// FormatHeader is the HeaderQuery value for names.
func FormatHeader(names []string) string { return strings.Join(names, ",") }
