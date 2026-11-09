// Package policydraft drafts an L3 policy from an OpenAPI document alone: one
// rule per operation, each with a risk level and the reason it was given.
// Clear signals in the document decide first; a model decides only what they
// cannot, and says when it is unsure so a person reviews that rule.
//
// It reads the document and nothing else. A benchmark's answers (which calls
// must not run without approval) are written independently of the policy, so
// the benchmark can judge it; a policy drafted from them would grade itself.
package policydraft

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/iml885203/lodog/internal/readiness"
	"github.com/iml885203/lodog/internal/spec"
)

// Source says what decided an operation's risk.
type Source string

// The deciders of a risk.
const (
	ByHeuristic Source = "heuristic"
	ByModel     Source = "model"
)

// Classification is the drafted risk of one operation.
type Classification struct {
	Method      string
	Path        string
	Risk        string // a risk name of the draft's risk set
	Why         string
	Source      Source
	NeedsReview bool
}

var (
	internalSegment = regexp.MustCompile(`(?i)^internal`)
	testSegment     = regexp.MustCompile(`^(?i:tests?)$|^(?i:test)[A-Z_-]`)
	credentialWord  = regexp.MustCompile(`(?i)(password|passwd|otp|unlock|token|secret|credential|apikey|2fa|mfa)`)
)

// heuristic is one signal that can settle an operation's class on its own.
type heuristic func(sig readiness.Signals, op spec.Operation) (class, why string, ok bool)

// heuristics run in order; the first that settles an operation decides.
// Path markers and credentials come before the read check so a GET under
// /internal or a GET that resets a password is never drafted as read.
// Irreversible comes before money: a payout is both, and the stricter level
// wins, because an approval given by mistake cannot be taken back.
var heuristics = []heuristic{declared, internalPath, testPath, credential, plainRead, irreversible, money}

// An unreviewed read (needsReview) is a model's guess, not a decision: it is
// not taken as the reason an operation may run without approval. Any other
// declared risk is kept even unreviewed, since it only asks for more care.
func declared(sig readiness.Signals, op spec.Operation) (string, string, bool) {
	if !slices.Contains(classes, op.AgentRisk) || (op.AgentRisk == ClassRead && (op.NeedsReview || sig.Mutating(op))) {
		return "", "", false
	}
	return op.AgentRisk, "declared by the document's x-agent.risk", true
}

func internalPath(_ readiness.Signals, op spec.Operation) (string, string, bool) {
	return segment(op, internalSegment, ClassInternal, "path segment %q marks a service-to-service endpoint")
}

func testPath(_ readiness.Signals, op spec.Operation) (string, string, bool) {
	return segment(op, testSegment, ClassTest, "path segment %q marks a test endpoint")
}

func segment(op spec.Operation, re *regexp.Regexp, class, why string) (string, string, bool) {
	for _, seg := range strings.Split(strings.Trim(op.Path, "/"), "/") {
		if re.MatchString(seg) {
			return class, fmt.Sprintf(why, seg), true
		}
	}
	return "", "", false
}

func credential(_ readiness.Signals, op spec.Operation) (string, string, bool) {
	word := credentialWord.FindString(op.Path + " " + op.OperationID + " " + op.Summary)
	if word == "" {
		return "", "", false
	}
	return ClassCredential, fmt.Sprintf("names a credential (%s)", strings.ToLower(word)), true
}

func plainRead(sig readiness.Signals, op spec.Operation) (string, string, bool) {
	if sig.MaybeMutating(op) {
		return "", "", false
	}
	return ClassRead, "a GET whose path names no action", true
}

var (
	// finalWord names an action that cannot be undone on any record.
	finalWord = []string{"delete", "remove", "purge", "destroy", "wipe", "terminate", "close", "payout"}
	// settleWord names an action that settles a movement of money: the money
	// has left, or the transaction is closed, once it runs.
	settleWord   = []string{"complete", "approve", "reject", "process", "rollback", "issue", "settle", "transfer", "refund"}
	wordBoundary = regexp.MustCompile(`([a-z0-9])([A-Z])`)
)

func irreversible(sig readiness.Signals, op spec.Operation) (string, string, bool) {
	if op.Method == "DELETE" {
		return ClassIrreversible, "a DELETE: a deleted record does not come back", true
	}
	words := actionWords(op.Path)
	if w, ok := firstOf(words, finalWord); ok {
		return ClassIrreversible, fmt.Sprintf("the path names an action that cannot be undone (%s)", w), true
	}
	if m, isMoney := sig.Money(op); isMoney {
		if w, ok := firstOf(words, settleWord); ok {
			return ClassIrreversible, fmt.Sprintf("names money (%s) and settles it (%s)", m, w), true
		}
	}
	return "", "", false
}

// actionWords splits the last two literal segments of path into lower-case
// words: /payment/transaction/withdraw/complete gives withdraw, complete.
func actionWords(path string) []string {
	var segs []string
	for _, seg := range strings.Split(strings.Trim(path, "/"), "/") {
		if !strings.HasPrefix(seg, "{") {
			segs = append(segs, seg)
		}
	}
	segs = segs[max(0, len(segs)-2):]
	split := wordBoundary.ReplaceAllString(strings.Join(segs, " "), "$1 $2")
	return strings.FieldsFunc(strings.ToLower(split), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
}

func firstOf(words, want []string) (string, bool) {
	for _, w := range words {
		if slices.Contains(want, w) {
			return w, true
		}
	}
	return "", false
}

func money(sig readiness.Signals, op spec.Operation) (string, string, bool) {
	word, ok := sig.Money(op)
	if !ok || !sig.Mutating(op) {
		return "", "", false
	}
	return ClassMoney, fmt.Sprintf("changes state and names money (%s)", word), true
}

// classify settles op by heuristics alone, giving it the risk of rs that
// takes the class they found; ok is false when none applies.
func classify(sig readiness.Signals, rs RiskSet, op spec.Operation) (Classification, bool) {
	for _, h := range heuristics {
		if class, why, ok := h(sig, op); ok {
			return Classification{Method: op.Method, Path: op.Path, Risk: rs.riskOf(class), Why: "heuristic: " + why, Source: ByHeuristic}, true
		}
	}
	return Classification{}, false
}
