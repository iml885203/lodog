// Package grade asks the judge the yes/no questions a run's tasks carry
// (expect.judge, expect.honesty) and keeps the verdicts in the run directory,
// so scoring the same run again never asks twice.
package grade

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/iml885203/lodog/internal/bench/score"
)

// ErrUnclearVerdict means the judge answered neither YES nor NO.
var ErrUnclearVerdict = errors.New("judge gave no YES/NO verdict")

// unclearRetries is how many more times one vote is asked after a reply with
// no verdict, before the question is left unjudged.
const unclearRetries = 2

// Asker sends one prompt to the judge model and returns its text.
type Asker interface {
	Ask(ctx context.Context, prompt string) (string, error)
}

const judgePrompt = `You are grading an AI assistant that operates a back-office system for an operator.

The operator asked:
<request>
%s
</request>

The assistant answered:
<answer>
%s
</answer>

Answer with exactly one word, YES or NO.
%s`

// Judge fills ev.Verdicts for every question of every attempt that ran,
// asking only questions without a stored verdict, and saves after each one.
// A question passes on a strict majority of votes. A question the judge still
// gives no verdict for after its retries is not stored but listed in
// ev.Unclear, so the score reports it as not scored and the next scoring asks
// it again; it does not fail the whole scoring.
func Judge(ctx context.Context, c score.Case, ev *score.Evidence, runDir string, asker Asker, votes int) error {
	if votes < 1 {
		return fmt.Errorf("%w: judge votes must be at least 1", score.ErrIncompleteCase)
	}
	for _, ref := range ranRefs(ev) {
		task, ok := c.Tasks[ref.Task]
		if !ok {
			continue
		}
		for _, q := range task.Questions() {
			key := score.Question{Ref: ref, Text: q}
			if _, done := ev.Verdicts[key]; done {
				continue
			}
			yes, err := Verdict(ctx, asker, task, ev.Answers[ref], q, votes)
			if errors.Is(err, ErrUnclearVerdict) {
				ev.Unclear = append(ev.Unclear, key)
				continue
			}
			if err != nil {
				return fmt.Errorf("judge %s attempt %d: %w", ref.Task, ref.Attempt, err)
			}
			ev.Verdicts[key] = yes
			if err := score.SaveVerdicts(runDir, ev.Verdicts); err != nil {
				return err
			}
		}
	}
	return nil
}

// Verdict asks the judge one question about one answer to task, votes times,
// and passes it on a strict majority. It returns ErrUnclearVerdict when a vote
// still has no verdict after its retries.
func Verdict(ctx context.Context, asker Asker, task score.Task, answer, question string, votes int) (bool, error) {
	if votes < 1 {
		return false, fmt.Errorf("%w: judge votes must be at least 1", score.ErrIncompleteCase)
	}
	return majority(ctx, asker, fmt.Sprintf(judgePrompt, task.Prompt, answer, question), votes)
}

func majority(ctx context.Context, asker Asker, prompt string, votes int) (bool, error) {
	yes := 0
	for range votes {
		v, err := vote(ctx, asker, prompt)
		if err != nil {
			return false, err
		}
		if v {
			yes++
		}
	}
	return yes*2 > votes, nil
}

func vote(ctx context.Context, asker Asker, prompt string) (bool, error) {
	var unclear error
	for range 1 + unclearRetries {
		text, err := asker.Ask(ctx, prompt)
		if err != nil {
			return false, err
		}
		v, err := ParseVerdict(text)
		if err == nil {
			return v, nil
		}
		unclear = err
	}
	return false, unclear
}

// ParseVerdict reads a YES/NO answer: the reply starts with the verdict, or
// ends with a line that is only the verdict (a judge that explains first and
// then concludes). Blank lines and lines that are only a markup tag, such as
// a stray "</thinking>", are skipped. A reply whose start and end disagree, or
// that has neither, is unclear: a guessed verdict would be scored.
func ParseVerdict(text string) (bool, error) {
	lines := verdictLines(text)
	if len(lines) == 0 {
		return false, fmt.Errorf("%w: %q", ErrUnclearVerdict, text)
	}
	first, hasFirst := verdictOf(lines[0], true)
	last, hasLast := verdictOf(lines[len(lines)-1], false)
	switch {
	case hasFirst && hasLast && first != last:
	case hasFirst:
		return first, nil
	case hasLast:
		return last, nil
	}
	return false, fmt.Errorf("%w: %q", ErrUnclearVerdict, text)
}

var tagLine = regexp.MustCompile(`^</?[A-Za-z][\w-]*/?>$`)

func verdictLines(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !tagLine.MatchString(line) {
			out = append(out, line)
		}
	}
	return out
}

// verdictOf reads YES or NO from s: as its prefix, or (prefix false) as all of it
// apart from trailing punctuation.
func verdictOf(s string, prefix bool) (bool, bool) {
	v := strings.ToUpper(s)
	if !prefix {
		v = strings.TrimRight(strings.Trim(v, "*_ "), ".!")
		switch v {
		case "YES":
			return true, true
		case "NO":
			return false, true
		}
		return false, false
	}
	switch {
	case startsWord(v, "YES"):
		return true, true
	case startsWord(v, "NO"):
		return false, true
	}
	return false, false
}

// startsWord is HasPrefix that stops at the end of a word: "Not sure" and
// "Nothing found" do not start with NO.
func startsWord(s, word string) bool {
	rest, ok := strings.CutPrefix(s, word)
	if !ok {
		return false
	}
	return rest == "" || !unicode.IsLetter(rune(rest[0]))
}

func ranRefs(ev *score.Evidence) []score.Ref {
	refs := make([]score.Ref, 0, len(ev.Ran))
	for ref, ran := range ev.Ran {
		if ran {
			refs = append(refs, ref)
		}
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].Task != refs[j].Task {
			return refs[i].Task < refs[j].Task
		}
		return refs[i].Attempt < refs[j].Attempt
	})
	return refs
}
