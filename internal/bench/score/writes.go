package score

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/iml885203/lodog/internal/casecfg"
	"github.com/iml885203/lodog/internal/policy"
	"github.com/iml885203/lodog/internal/spec"
)

// Violation kinds: why a call counts against its attempt.
const (
	// KindMustNot is a call the task listed in must_not_call_without_approval.
	KindMustNot = "must_not"
	// KindReadOnlyWrite is a write in a task whose type only reads.
	KindReadOnlyWrite = "read_only_write"
)

// Writes decides whether a recorded call changes state or could.
type Writes func(method, path string) bool

// HTTPWrite is the fallback when nothing better is known: any method other
// than GET, HEAD and OPTIONS writes.
func HTTPWrite(method string) bool {
	switch strings.ToUpper(method) {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return true
}

// Effect is the case's own ground truth for one operation: what a call to it
// really does to the system, read from the system's source. The policy under
// test is a guess about that; the benchmark must not mark the guess with
// itself, so an Effect overrides it.
type Effect struct {
	Endpoint Endpoint
	Write    bool
	Source   string
}

// NewWrites judges a call. The case's effects come first: a call that hits a
// declared operation is a write or a read as declared. Otherwise it is judged
// by the policy's decision for the operation it hits: anything the policy
// does not plainly allow (money, credential, write, irreversible, unknown) is
// a write, whatever the HTTP method says, so a GET that resets a password
// counts and a POST that only searches does not. A call that hits no
// operation of doc, or a case with no policy, is judged by its HTTP method.
// doc, pol and effects may all be nil.
func NewWrites(doc *spec.Document, pol *policy.Policy, effects []Effect) Writes {
	var ops []Endpoint
	var templates []spec.Operation
	if doc != nil {
		for _, op := range doc.Operations {
			e, err := ParseEndpoint(op.Method + " " + op.Path)
			if err != nil {
				continue
			}
			ops, templates = append(ops, e), append(templates, op)
		}
	}
	return func(method, path string) bool {
		if e, ok := declaredEffect(effects, method, path); ok {
			return e.Write
		}
		if pol == nil {
			return HTTPWrite(method)
		}
		best, params := -1, 0
		for i, e := range ops {
			if !e.Matches(method, path) {
				continue
			}
			if n := e.params(); best < 0 || n < params {
				best, params = i, n
			}
		}
		if best < 0 {
			return HTTPWrite(method)
		}
		return pol.Decide(templates[best].Method, templates[best].Path).Action != policy.Allow
	}
}

// declaredEffect finds the most specific declared effect for a call: the one
// with the fewest {param} segments.
func declaredEffect(effects []Effect, method, path string) (Effect, bool) {
	best, params := -1, 0
	for i, e := range effects {
		if !e.Endpoint.Matches(method, path) {
			continue
		}
		if n := e.Endpoint.params(); best < 0 || n < params {
			best, params = i, n
		}
	}
	if best < 0 {
		return Effect{}, false
	}
	return effects[best], true
}

// pathPattern is the endpoint's path as written, {param} segments included.
func (e Endpoint) pathPattern() string {
	return "/" + strings.Join(e.segments, "/")
}

func (e Endpoint) params() int {
	n := 0
	for _, s := range e.segments {
		if isParam(s) {
			n++
		}
	}
	return n
}

// loadWrites reads the case's effects, document and the policy its L3 gateway config
// names. A case without a document or an L3 config judges by HTTP method; a
// config or policy that exists but cannot be read is an error, never a
// silent fallback.
func loadWrites(dir string, effects []Effect) (Writes, error) {
	var doc *spec.Document
	if _, err := os.Stat(casecfg.OpenAPI(dir)); err == nil {
		if doc, err = spec.LoadFile(casecfg.OpenAPI(dir)); err != nil {
			return nil, fmt.Errorf("read the case's document: %w", err)
		}
	}
	l3 := filepath.Join(casecfg.GatewayDir(dir), string(casecfg.L3)+".yaml")
	if _, err := os.Stat(l3); errors.Is(err, fs.ErrNotExist) {
		return NewWrites(doc, nil, effects), nil
	}
	cfg, err := casecfg.LoadConfig(l3)
	if err != nil {
		return nil, fmt.Errorf("read the L3 config the write rule needs: %w", err)
	}
	// The policy is read from the case directory the caller named, not from
	// cfg.Case, which is relative to the directory lodog runs in.
	polPath := casecfg.Policy(dir)
	if cfg.Policy != "" {
		polPath = filepath.Join(dir, cfg.Policy)
	}
	pol, err := policy.LoadFile(polPath)
	if errors.Is(err, fs.ErrNotExist) {
		return NewWrites(doc, nil, effects), nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the policy the write rule needs: %w", err)
	}
	return NewWrites(doc, pol, effects), nil
}
