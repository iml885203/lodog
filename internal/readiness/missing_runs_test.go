package readiness

import (
	"strings"
	"testing"
)

// calls turns "TASK METHOD /path STATUS" lines into one attempt's calls.
func calls(lines ...string) []RunCall {
	out := make([]RunCall, len(lines))
	for i, l := range lines {
		f := strings.Fields(l)
		status := 200
		if len(f) > 3 && f[3] != "200" {
			status = 400
		}
		out[i] = RunCall{TaskID: f[0], Attempt: 1, Seq: i + 1, Method: f[1], Path: f[2], Status: status}
	}
	return out
}

func withRuns(t *testing.T, v View, cs []RunCall) (*L4, *L4) {
	t.Helper()
	before := Score(v).L4
	return before, WithRuns(before, v, []RunCalls{{ID: "r1", Calls: cs}})
}

func runLines(l4 *L4) []string {
	it, _ := item(l4, missingID)
	var out []string
	for _, e := range it.Endpoints {
		out = append(out, e.Runs...)
	}
	return out
}

var txDoc = []string{"POST /tx/reject", "GET /txs paged", "GET /players/{id}"}

func TestAWriteFollowedByAListScanOrByNothingIsEvidence(t *testing.T) {
	v := View{Doc: paths(t, txDoc...)}
	before, after := withRuns(t, v, calls(
		"T1 POST /tx/reject",
		"T1 GET /txs",
		"T2 POST /tx/reject",
		"T3 POST /tx/reject",
		"T3 GET /players/7",
		"_state_check GET /txs",
	))
	got := strings.Join(runLines(after), "\n")
	for _, want := range []string{
		"r1 T1-1：POST /tx/reject 之後翻列表找回剛寫的那筆",
		"r1 T2-1：POST /tx/reject 之後沒有任何讀取",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("evidence is missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "T3") || strings.Contains(got, "_state_check") {
		t.Errorf("a write read back some other way, or the runner's own call, is not evidence:\n%s", got)
	}
	if len(runLines(before)) != 0 {
		t.Error("WithRuns changed the L4 it was given")
	}
	it, _ := item(after, missingID)
	if !strings.Contains(it.Measured, "run") || strings.Contains(it.Instruction, "r1 T1") {
		t.Errorf("measured = %q; the instruction must not carry run evidence: %s", it.Measured, it.Instruction)
	}
}

func TestAPagedListCalledTwiceWithAnswersAddsTheRecordGETItLacks(t *testing.T) {
	v := View{Doc: paths(t, "GET /events paged", "GET /audit paged")}
	_, after := withRuns(t, v, calls(
		"T1 GET /events",
		"T1 GET /events",
		"T2 GET /audit 400",
		"T2 GET /audit",
	))
	it, ok := item(after, missingID)
	if !ok || len(it.Endpoints) != 1 || it.Endpoints[0].Path != "/events/{id}" || it.Endpoints[0].List != "GET /events" {
		t.Fatalf("item = %+v, want the record GET of /events only (a retry after an error is not a scan)", it)
	}
	if !strings.Contains(it.Endpoints[0].Runs[0], "成功叫了 2 次 GET /events") {
		t.Errorf("runs = %v", it.Endpoints[0].Runs)
	}
}

func TestAScanOfAListWithARecordGETSaysNothingAboutTheAPI(t *testing.T) {
	for name, c := range map[string]struct {
		doc  []string
		call string
	}{
		"record under it":  {[]string{"GET /users paged", "GET /users/{id}"}, "T1 GET /users"},
		"record beside it": {[]string{"GET /customer/search paged", "GET /customer/{id}"}, "T1 GET /customer/search"},
		"not paged":        {[]string{"GET /users"}, "T1 GET /users"},
	} {
		before, after := withRuns(t, View{Doc: paths(t, c.doc...)}, calls(c.call, c.call))
		if after != before {
			t.Errorf("%s: the agent's own choice became evidence: %v", name, runLines(after))
		}
	}
}

func TestACallIsMatchedToTheMostLiteralTemplate(t *testing.T) {
	m := newMatcher(paths(t, "GET /content/{id}", "GET /content/list paged"))
	op, ok := m.match("GET", "/Content/list")
	if !ok || op.Path != "/content/list" {
		t.Fatalf("matched %+v", op)
	}
	if op, _ := m.match("GET", "/content/42"); op.Path != "/content/{id}" {
		t.Fatalf("matched %+v", op)
	}
	if _, ok := m.match("DELETE", "/content/42"); ok {
		t.Fatal("matched an operation the document does not have")
	}
}

func TestEvidenceIsCappedPerEndpoint(t *testing.T) {
	v := View{Doc: paths(t, txDoc...)}
	var lines []string
	for _, task := range []string{"T1", "T2", "T3", "T4", "T5"} {
		lines = append(lines, task+" POST /tx/reject")
	}
	_, after := withRuns(t, v, calls(lines...))
	got := runLines(after)
	if len(got) != maxRunEvidence+1 || got[maxRunEvidence] != "…還有 2 筆" {
		t.Fatalf("runs = %v", got)
	}
}
