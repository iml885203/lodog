package score

import (
	"errors"
	"fmt"
	"strings"
)

// ErrBadEndpoint is returned for an endpoint string that is not "METHOD /path".
var ErrBadEndpoint = errors.New("endpoint must be \"METHOD /path\"")

// Endpoint is one entry from answers.yaml or case.yaml, such as
// "PATCH /api/customer/crmlogs/{logId}". A {name} segment matches any value.
// The query string is ignored when matching: the lists it is matched against
// name operations, not argument values.
type Endpoint struct {
	Method   string
	segments []string
}

// ParseEndpoint parses "METHOD /path[?query]".
func ParseEndpoint(s string) (Endpoint, error) {
	method, rest, ok := strings.Cut(strings.TrimSpace(s), " ")
	if !ok || method == "" || !strings.HasPrefix(rest, "/") {
		return Endpoint{}, fmt.Errorf("%w: %q", ErrBadEndpoint, s)
	}
	path, _, _ := strings.Cut(rest, "?")
	return Endpoint{Method: strings.ToUpper(method), segments: split(path)}, nil
}

// Matches reports whether a recorded call hits this endpoint. Comparison is
// case-insensitive because the legacy system's routing is.
func (e Endpoint) Matches(method, path string) bool {
	if !strings.EqualFold(e.Method, method) {
		return false
	}
	got := split(path)
	if len(got) != len(e.segments) {
		return false
	}
	for i, want := range e.segments {
		if isParam(want) {
			continue
		}
		if !strings.EqualFold(want, got[i]) {
			return false
		}
	}
	return true
}

func (e Endpoint) String() string {
	return e.Method + " /" + strings.Join(e.segments, "/")
}

func split(path string) []string {
	return strings.Split(strings.Trim(path, "/"), "/")
}

func isParam(seg string) bool {
	return strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}")
}

func parseAll(raw []string) ([]Endpoint, error) {
	out := make([]Endpoint, 0, len(raw))
	for _, s := range raw {
		e, err := ParseEndpoint(s)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

func matchesAny(list []Endpoint, method, path string) bool {
	for _, e := range list {
		if e.Matches(method, path) {
			return true
		}
	}
	return false
}
