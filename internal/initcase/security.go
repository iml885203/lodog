package initcase

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/iml885203/lodog/internal/casecfg"
)

// authHint is what the document's securitySchemes say about how to sign in,
// reduced to the fields a gateway auth block takes.
type authHint struct {
	Type     string // a casecfg.Auth* type
	From     string // the securitySchemes entry it came from
	Header   string // api-key in a header
	Query    string // api-key in the query string
	TokenURL string // oauth2 client credentials
	Scope    string
}

// scheme is one securitySchemes entry: a hint when lodog can use it, else why not.
type scheme struct {
	name   string
	hint   *authHint
	reason string
	desc   string // how the document describes it, for the comment
}

// readSchemes classifies the document's securitySchemes. Schemes the top-level
// security requirement names come first, in that order, then the rest by name,
// so the scheme the document applies everywhere wins over a side option.
func readSchemes(raw []byte) []scheme {
	var d struct {
		Security   []map[string]any `json:"security"`
		Components struct {
			SecuritySchemes map[string]map[string]any `json:"securitySchemes"`
		} `json:"components"`
	}
	if json.Unmarshal(raw, &d) != nil {
		return nil
	}
	defs := d.Components.SecuritySchemes
	var order []string
	seen := map[string]bool{}
	for _, req := range d.Security {
		names := make([]string, 0, len(req))
		for n := range req {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			if _, ok := defs[n]; ok && !seen[n] {
				seen[n] = true
				order = append(order, n)
			}
		}
	}
	rest := make([]string, 0, len(defs))
	for n := range defs {
		if !seen[n] {
			rest = append(rest, n)
		}
	}
	sort.Strings(rest)
	order = append(order, rest...)

	out := make([]scheme, 0, len(order))
	for _, n := range order {
		out = append(out, classify(n, defs[n]))
	}
	return out
}

func str(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

func classify(name string, s map[string]any) scheme {
	switch typ := str(s, "type"); strings.ToLower(typ) {
	case "apikey":
		switch in := str(s, "in"); in {
		case "header":
			return scheme{
				name: name, desc: "apiKey in header " + str(s, "name"),
				hint: &authHint{Type: casecfg.AuthAPIKey, From: name, Header: str(s, "name")},
			}
		case "query":
			return scheme{
				name: name, desc: "apiKey in query " + str(s, "name"),
				hint: &authHint{Type: casecfg.AuthAPIKey, From: name, Query: str(s, "name")},
			}
		case "cookie":
			return scheme{
				name: name, desc: "apiKey in cookie " + str(s, "name"),
				hint: &authHint{Type: casecfg.AuthCookieSession, From: name},
			}
		default:
			return scheme{name: name, desc: "apiKey", reason: fmt.Sprintf("apiKey in %q is not a place lodog can send a key", in)}
		}
	case "http":
		switch sch := strings.ToLower(str(s, "scheme")); sch {
		case "bearer":
			return scheme{name: name, desc: "http bearer", hint: &authHint{Type: casecfg.AuthBearer, From: name}}
		default:
			return scheme{name: name, desc: "http " + sch, reason: "http " + sch + " is not supported yet"}
		}
	case "oauth2":
		flows, _ := s["flows"].(map[string]any)
		cc, ok := flows["clientCredentials"].(map[string]any)
		if !ok {
			names := make([]string, 0, len(flows))
			for f := range flows {
				names = append(names, f)
			}
			sort.Strings(names)
			return scheme{
				name: name, desc: "oauth2 " + strings.Join(names, ", "),
				reason: "only the clientCredentials flow is supported (a person signs in for the other flows)",
			}
		}
		scopes, _ := cc["scopes"].(map[string]any)
		list := make([]string, 0, len(scopes))
		for sc := range scopes {
			list = append(list, sc)
		}
		sort.Strings(list)
		url := str(cc, "tokenUrl")
		if url != "" && !strings.Contains(url, "://") && !strings.HasPrefix(url, "/") {
			url = "/" + url
		}
		return scheme{
			name: name, desc: "oauth2 clientCredentials",
			hint: &authHint{Type: casecfg.AuthOAuth2, From: name, TokenURL: url, Scope: strings.Join(list, " ")},
		}
	default:
		return scheme{name: name, desc: typ, reason: fmt.Sprintf("type %q is not supported", typ)}
	}
}

// inferAuth picks the auth type and its hint. An explicit type wins and takes
// the fields of the first scheme of that type; without one, the first usable
// scheme decides, and a document with none falls back to cookie-session, the
// type for systems that sign in with a form.
func inferAuth(schemes []scheme, explicit string) (typ string, hint *authHint, notes []string) {
	var used *scheme
	for i := range schemes {
		s := &schemes[i]
		if s.hint == nil {
			continue
		}
		if (explicit == "" || s.hint.Type == explicit) && used == nil {
			used = s
		}
	}
	switch {
	case used != nil:
		typ, hint = used.hint.Type, used.hint
		notes = append(notes, fmt.Sprintf("auth.type %s is read from securitySchemes.%s (%s).", typ, used.name, used.desc))
	case explicit != "":
		typ = explicit
		notes = append(notes, fmt.Sprintf("auth.type %s was asked for; the document declares no matching scheme, so the fields below are generic.", explicit))
	default:
		typ = casecfg.AuthCookieSession
		if len(schemes) == 0 {
			notes = append(notes, "The document declares no securitySchemes, so auth.type is a guess: cookie-session. Change it if the system signs in differently.")
		} else {
			notes = append(notes, "None of the document's securitySchemes is one lodog supports, so auth.type is a guess: cookie-session.")
		}
	}
	for i := range schemes {
		s := &schemes[i]
		if s == used {
			continue
		}
		if s.hint != nil {
			notes = append(notes, fmt.Sprintf("Also declared: securitySchemes.%s (%s), usable as auth.type %s.", s.name, s.desc, s.hint.Type))
		} else {
			notes = append(notes, fmt.Sprintf("Also declared: securitySchemes.%s (%s), not usable: %s.", s.name, s.desc, s.reason))
		}
	}
	return typ, hint, notes
}
