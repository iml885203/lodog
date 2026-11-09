package grade

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/iml885203/lodog/internal/bench/score"
)

type scriptedAsker struct {
	answers []string
	prompts []string
}

func (a *scriptedAsker) Ask(_ context.Context, prompt string) (string, error) {
	a.prompts = append(a.prompts, prompt)
	next := a.answers[0]
	a.answers = a.answers[1:]
	return next, nil
}

func oneTask() score.Case {
	return score.Case{Tasks: map[string]score.Task{"N": {
		ID: "N", Type: score.TypeNotFound, Prompt: "查玩家 bench_eve 的存款",
		Expect: score.Expect{Judge: []string{"Says missing?"}},
	}}}
}

func evidence() score.Evidence {
	ref := score.Ref{Task: "N", Attempt: 1}
	return score.Evidence{
		Ran: map[score.Ref]bool{ref: true}, Answers: map[score.Ref]string{ref: "沒有這個玩家"},
		Verdicts: map[score.Question]bool{},
	}
}

func TestAQuestionPassesOnAStrictMajority(t *testing.T) {
	for answers, want := range map[string]bool{"YES,NO,YES": true, "NO,YES,NO": false} {
		ev := evidence()
		asker := &scriptedAsker{answers: strings.Split(answers, ",")}
		if err := Judge(t.Context(), oneTask(), &ev, t.TempDir(), asker, 3); err != nil {
			t.Fatal(err)
		}
		got := ev.Verdicts[score.Question{Ref: score.Ref{Task: "N", Attempt: 1}, Text: "Says missing?"}]
		if got != want || len(asker.prompts) != 3 {
			t.Errorf("%s: verdict %v after %d asks, want %v", answers, got, len(asker.prompts), want)
		}
		if !strings.Contains(asker.prompts[0], "bench_eve") || !strings.Contains(asker.prompts[0], "沒有這個玩家") {
			t.Errorf("prompt lacks the request or the answer: %s", asker.prompts[0])
		}
	}
}

func TestAStoredVerdictIsNeverAskedAgain(t *testing.T) {
	dir := t.TempDir()
	ev := evidence()
	if err := Judge(t.Context(), oneTask(), &ev, dir, &scriptedAsker{answers: []string{"YES"}}, 1); err != nil {
		t.Fatal(err)
	}
	stored, err := score.LoadVerdicts(dir)
	if err != nil {
		t.Fatal(err)
	}
	again := evidence()
	again.Verdicts = stored
	asker := &scriptedAsker{}
	if err := Judge(t.Context(), oneTask(), &again, dir, asker, 1); err != nil {
		t.Fatal(err)
	}
	if len(asker.prompts) != 0 || len(stored) != 1 {
		t.Fatalf("asked %d times with %d stored verdicts", len(asker.prompts), len(stored))
	}
}

func TestAnUnclearAnswerIsAskedAgain(t *testing.T) {
	ev := evidence()
	asker := &scriptedAsker{answers: []string{"probably", "**YES**"}}
	if err := Judge(t.Context(), oneTask(), &ev, t.TempDir(), asker, 1); err != nil {
		t.Fatal(err)
	}
	q := score.Question{Ref: score.Ref{Task: "N", Attempt: 1}, Text: "Says missing?"}
	if !ev.Verdicts[q] || len(asker.prompts) != 2 || len(ev.Unclear) != 0 {
		t.Fatalf("verdict %v after %d asks, unclear %v", ev.Verdicts[q], len(asker.prompts), ev.Unclear)
	}
}

func TestAQuestionStillUnclearAfterItsRetriesIsLeftUnjudgedWithoutFailingTheScoring(t *testing.T) {
	dir := t.TempDir()
	ev := evidence()
	asker := &scriptedAsker{answers: []string{"probably", "maybe", "hard to say"}}
	if err := Judge(t.Context(), oneTask(), &ev, dir, asker, 1); err != nil {
		t.Fatalf("got %v, want the scoring to go on", err)
	}
	q := score.Question{Ref: score.Ref{Task: "N", Attempt: 1}, Text: "Says missing?"}
	if len(asker.prompts) != 1+unclearRetries || len(ev.Unclear) != 1 || ev.Unclear[0] != q {
		t.Fatalf("%d asks, unclear %v", len(asker.prompts), ev.Unclear)
	}
	if _, stored := ev.Verdicts[q]; stored {
		t.Fatal("an unclear question was stored, so scoring again would never ask it")
	}
}

func TestAnAskerErrorStillStopsGrading(t *testing.T) {
	ev := evidence()
	boom := errors.New("claude: exit status 1")
	err := Judge(t.Context(), oneTask(), &ev, t.TempDir(), failingAsker{boom}, 1)
	if !errors.Is(err, boom) {
		t.Fatalf("got %v, want the asker's error", err)
	}
}

type failingAsker struct{ err error }

func (a failingAsker) Ask(context.Context, string) (string, error) {
	return "", a.err
}

func TestAVerdictMayCloseAnExplanationButNeverContradictIt(t *testing.T) {
	for text, want := range map[string]bool{
		"YES": true,
		"no.": false,
		"The answer doesn't mention alice.\n\nNO": false,
		"It names carol and alice.\nYES.\n":       true,
		"Checked both.\n**NO**":                   false,
		"</thinking_mode>\n\nYES":                 true,
		"<thinking>\n\nNO\n</thinking>":           false,
		"The answer says no tier.\n\n**YES**":     true,
	} {
		got, err := ParseVerdict(text)
		if err != nil || got != want {
			t.Errorf("%q: got %v, %v, want %v", text, got, err, want)
		}
	}
	for _, text := range []string{"maybe", "No, it does not.\nYES", "Not sure.\nIt depends on NO or YES here", "", "</thinking_mode>"} {
		if _, err := ParseVerdict(text); !errors.Is(err, ErrUnclearVerdict) {
			t.Errorf("%q: got %v, want ErrUnclearVerdict", text, err)
		}
	}
}
