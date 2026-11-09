// Package readiness scores how ready an API is for an agent from its
// documents alone, without running anything: the static 30 points of a level
// (docs/static-score.md). It reads the OpenAPI document and, for a gateway
// level, the policy and identity the gateway adds. The checks and their
// weights are fixed in code so scores of different systems compare.
package readiness

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/iml885203/lodog/internal/spec"
)

// The generic vocabularies every system starts from. They name what any
// backoffice API does, not what one business calls things; a system adds its
// own words in its case.yaml (Config).
//
// An action word is only a verb that, on a GET, nearly always means the call
// changes state. A word that is also a noun (lock, trigger, transfer, refund,
// payout, sync: GET /locks lists them) or that often names a harmless preview
// or computation (generate, as in generate-example-token) is left out: a wrong
// guess makes a read look dangerous, which costs the score and hides the real
// ones. A system that really uses such a word as an action adds it in its case;
// the score still leaves out a preview of it (defaultPreviewWords).
var (
	// suspectWords are the words left out of defaultActionWords because a GET
	// with one is often harmless. The score does not count them (a wrong guess
	// there is an unfair deduction), but a policy must not let such a GET
	// through as a read (see MaybeMutating).
	suspectWords = []string{"generate", "lock", "trigger", "transfer", "refund", "payout", "sync"}

	defaultActionWords = []string{
		"reset", "send", "resend", "approve", "reject", "cancel", "unlock",
		"enable", "disable", "pay", "delete", "remove",
		"purge", "revoke", "notify", "activate", "deactivate",
	}
	// defaultPreviewWords name a call that returns a preview of an action
	// instead of doing it: generate-example-access-token, send/preview. The
	// score does not count such a GET as changing state; policy draft and
	// bench draft still do, because there a wrong guess is an unapproved call.
	defaultPreviewWords = []string{"example", "preview", "sample", "template"}

	defaultMoneyWords = []string{
		"payment", "pay", "transfer", "refund", "payout", "transaction", "charge", "invoice",
		"billing", "balance", "credit", "debit",
	}
)

var wordBoundary = regexp.MustCompile(`([a-z0-9])([A-Z])`)

// Paging parameters, by what they do once a name is lower-cased with `_`, `-`
// and a leading `$` removed. A strong name pages a list by itself. A weak name
// is too common alone (max, first, size, count, before) and counts only when the
// operation also has a parameter of the other kind: a position (where to start)
// with a size (how many), such as first+max, skip+take, after+count.
var (
	strongPosition = wordSet([]string{"page", "pagenumber", "pageindex", "pagenum", "offset", "cursor", "pagetoken", "nexttoken", "startingafter", "endingbefore"})
	strongSize     = wordSet([]string{"pagesize", "perpage", "limit", "maxresults"})
	weakPosition   = wordSet([]string{"first", "skip", "start", "after", "before", "from", "startindex"})
	weakSize       = wordSet([]string{"max", "take", "size", "count", "top", "rows", "pagelen"})
)

// Config is the words a system adds to the generic vocabularies, under
// `signals:` in its case.yaml. Each entry is one word.
type Config struct {
	// ActionWords name a path segment that acts on something, so a GET with
	// one is a read that may change state.
	ActionWords []string `yaml:"action_words"`
	// MoneyWords mark an operation that touches money or value.
	MoneyWords []string `yaml:"money_words"`
	// PreviewWords name a path segment that returns a preview of the action
	// beside it, so the score does not count that GET (ScoreSideEffectGET).
	PreviewWords []string `yaml:"preview_words"`
}

// Signals is the vocabulary that turns an operation's path and summary into
// guesses: does it act, does it touch money. A guess is only for an
// operation with no declared risk: an x-agent.risk (from an L1 overlay)
// always wins over the words.
//
// The zero value is the generic vocabulary.
type Signals struct {
	action, money, preview map[string]bool
}

// DefaultSignals is the generic vocabulary.
func DefaultSignals() Signals {
	return newSignals(Config{})
}

// NewSignals is the generic vocabulary plus the words in c.
func NewSignals(c Config) (Signals, error) {
	for _, w := range slices.Concat(c.ActionWords, c.MoneyWords, c.PreviewWords) {
		if !singleWord.MatchString(w) {
			return Signals{}, fmt.Errorf("signals: %q is not one word of letters and digits", w)
		}
	}
	return newSignals(c), nil
}

var singleWord = regexp.MustCompile(`^[A-Za-z0-9]+$`)

func newSignals(c Config) Signals {
	return Signals{
		action:  wordSet(defaultActionWords, c.ActionWords),
		money:   wordSet(defaultMoneyWords, c.MoneyWords),
		preview: wordSet(defaultPreviewWords, c.PreviewWords),
	}
}

func wordSet(lists ...[]string) map[string]bool {
	set := map[string]bool{}
	for _, l := range lists {
		for _, w := range l {
			set[strings.ToLower(w)] = true
		}
	}
	return set
}

// LoadSignals reads the signals of the case in dir. A case with no case.yaml
// or no `signals:` uses the generic vocabulary.
func LoadSignals(dir string) (Signals, error) {
	data, err := os.ReadFile(filepath.Join(filepath.Clean(dir), "case.yaml"))
	if os.IsNotExist(err) {
		return DefaultSignals(), nil
	}
	if err != nil {
		return Signals{}, fmt.Errorf("read case signals: %w", err)
	}
	var f struct {
		Signals Config `yaml:"signals"`
	}
	if err := yaml.Unmarshal(data, &f); err != nil {
		return Signals{}, fmt.Errorf("parse %s/case.yaml: %w", dir, err)
	}
	return NewSignals(f.Signals)
}

func (s Signals) orDefault() Signals {
	if s.action == nil {
		return DefaultSignals()
	}
	return s
}

// Whether a call changes state is judged in one place, here. Three questions
// differ on purpose, from strictest to most lenient:
//
//   - MaybeMutating: may this call change state? The policy side: a wrong
//     "no" is an unapproved dangerous call, so every doubt answers yes.
//   - Mutating: does this call name a state change? The drafting side, where
//     a wrong "yes" only costs one question or one approval, so a preview
//     still counts but a suspect word alone does not.
//   - ScoreMutating: does this call really change state? The static score, L4
//     and the report, where a wrong "yes" is an unfair deduction or a false
//     alarm, so a preview and a harmless-looking GET do not count.
//
// All three start from MethodMutates, so the rule "only a GET reads" exists
// once.

// MethodMutates reports whether the HTTP method alone makes a call a change:
// every method but GET. A GET is then judged by its path, declaration and
// parameters (Mutating, MaybeMutating, ScoreMutating).
func MethodMutates(method string) bool { return method != "GET" }

// SideEffectGET reports whether op is a GET whose last path segments name an
// action, such as /customer/{id}/resetPassword: a read that may change state.
// An operation with a declared risk is not guessed: the declaration decides.
func (s Signals) SideEffectGET(op spec.Operation) (string, bool) {
	if MethodMutates(op.Method) || op.AgentRisk != "" {
		return "", false
	}
	return firstIn(words(lastSegments(op.Path)), s.orDefault().action)
}

// ScoreSideEffectGET is SideEffectGET for the static score and L4: an action
// whose own segment or the last segment names a preview (preview words) only
// shows what the action would do, so it is not counted. The preview must sit
// beside the action, not anywhere in the path: GET /templates/send still sends.
func (s Signals) ScoreSideEffectGET(op spec.Operation) (string, bool) {
	word, ok := s.SideEffectGET(op)
	if !ok {
		return "", false
	}
	sig := s.orDefault()
	segs := strings.Split(lastSegments(op.Path), "/")
	last := words(segs[len(segs)-1])
	for _, seg := range segs {
		ws := words(seg)
		if _, action := firstIn(ws, sig.action); !action {
			continue
		}
		if _, preview := firstIn(slices.Concat(ws, last), sig.preview); !preview {
			return word, true
		}
	}
	return "", false
}

// ScoreMutating is the lenient version, for the static score, L4 and the
// report: Mutating with a GET judged by ScoreSideEffectGET, so a preview of an
// action is not counted. Never use it to decide what runs without approval.
func (s Signals) ScoreMutating(op spec.Operation) bool {
	if !MethodMutates(op.Method) && op.AgentRisk == "" {
		_, ok := s.ScoreSideEffectGET(op)
		return ok
	}
	return s.Mutating(op)
}

// Money returns the money word in op's path, id or summary, if any.
func (s Signals) Money(op spec.Operation) (string, bool) {
	return firstIn(words(op.Path+" "+op.OperationID+" "+op.Summary), s.orDefault().money)
}

// Mutating is the middle version, for drafting policies and benchmark tasks:
// any non-GET, or a GET that is declared non-read or names an action. A
// preview of an action still counts; a suspect word or a money word alone does
// not (MaybeMutating holds those).
func (s Signals) Mutating(op spec.Operation) bool {
	if !MethodMutates(op.Method) && op.AgentRisk != "" {
		return op.AgentRisk != "read"
	}
	_, sideEffect := s.SideEffectGET(op)
	return MethodMutates(op.Method) || sideEffect
}

// MaybeMutating is the strict, fail-closed version, for deciding what an agent
// may call without approval. It also holds a GET that touches money,
// names a suspect word (refund, lock, generate…) in its path, operationId or
// summary, or takes an action-style query parameter (?action=, ?command=): the
// score leaves those out because a wrong guess costs points, but here a wrong
// guess means an unapproved dangerous call, while the other mistake only costs
// one approval. A declared read counts only once a person has reviewed it: a
// model's unreviewed guess (NeedsReview) can raise a risk, never lower it.
func (s Signals) MaybeMutating(op spec.Operation) bool {
	if MethodMutates(op.Method) {
		return true
	}
	if op.AgentRisk == "read" && !op.NeedsReview {
		return false
	}
	if op.AgentRisk != "" && op.AgentRisk != "read" {
		return true
	}
	if _, ok := firstIn(words(lastSegments(op.Path)), s.orDefault().action); ok {
		return true
	}
	if _, ok := s.Money(op); ok {
		return true
	}
	suspect := wordSet(suspectWords)
	if _, ok := firstIn(words(lastSegments(op.Path)), suspect); ok {
		return true
	}
	if _, ok := firstIn(words(op.OperationID+" "+op.Summary), mergeSets(suspect, s.orDefault().action)); ok {
		return true
	}
	for _, p := range op.Params {
		if p.In == "query" && actionParam.MatchString(p.Name) {
			return true
		}
	}
	return false
}

// actionParam names a query parameter that picks what the call does.
var actionParam = regexp.MustCompile(`(?i)^(action|act|command|cmd|op|operation|do)$`)

func mergeSets(sets ...map[string]bool) map[string]bool {
	out := map[string]bool{}
	for _, set := range sets {
		for w := range set {
			out[w] = true
		}
	}
	return out
}

// words splits text into lower-case words at case changes and punctuation:
// resetPassword gives reset, password.
func words(text string) []string {
	split := wordBoundary.ReplaceAllString(text, "$1 $2")
	return strings.FieldsFunc(strings.ToLower(split), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
}

// firstIn finds the first word that is in set, or is a plural of one (s, es).
func firstIn(ws []string, set map[string]bool) (string, bool) {
	for _, w := range ws {
		if set[w] || set[strings.TrimSuffix(w, "s")] || set[strings.TrimSuffix(w, "es")] {
			return w, true
		}
	}
	return "", false
}

// PagingParams returns op's query parameters that page a list.
func PagingParams(op spec.Operation) []spec.Param {
	type kind struct {
		p              spec.Param
		position, size bool
		weak           bool
	}
	var ks []kind
	var havePosition, haveSize bool
	for _, p := range op.Params {
		if p.In != "query" {
			continue
		}
		n := strings.ToLower(strings.NewReplacer("_", "", "-", "", "$", "").Replace(p.Name))
		k := kind{p: p}
		switch {
		case strongPosition[n]:
			k.position = true
		case strongSize[n]:
			k.size = true
		case weakPosition[n]:
			k.position, k.weak = true, true
		case weakSize[n]:
			k.size, k.weak = true, true
		default:
			continue
		}
		havePosition = havePosition || k.position
		haveSize = haveSize || k.size
		ks = append(ks, k)
	}
	var out []spec.Param
	for _, k := range ks {
		if !k.weak || k.position && haveSize || k.size && havePosition {
			out = append(out, k.p)
		}
	}
	return out
}

func lastSegments(path string) string {
	segs := strings.Split(strings.Trim(path, "/"), "/")
	if len(segs) > 2 {
		segs = segs[len(segs)-2:]
	}
	return strings.Join(segs, "/")
}

// SignalsOf is the vocabulary of the case in caseDir, or the generic one when
// there is no case.
func SignalsOf(caseDir string) (Signals, error) {
	if caseDir == "" {
		return DefaultSignals(), nil
	}
	return LoadSignals(caseDir)
}
