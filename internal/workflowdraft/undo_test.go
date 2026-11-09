package workflowdraft

import (
	"context"
	"strings"
	"testing"

	"github.com/iml885203/lodog/internal/spec"
	"github.com/iml885203/lodog/internal/workflow"
)

// A moderation API that is not Backoffice. POST and DELETE of /flags are each
// other's undo; /notes can be added to and an item patched but not removed;
// /exports starts a job and nothing in the document stops it.
const moderation = `{"openapi":"3.0.4","info":{"title":"moderation","version":"1"},"paths":{
 "/accounts":{"get":{"summary":"Search accounts","parameters":[{"name":"name","in":"query","schema":{"type":"string"}}]}},
 "/flags":{
  "post":{"summary":"Flag an account","requestBody":{"required":true,"content":{"application/json":{"schema":{"type":"object","properties":{"accountId":{"type":"integer"},"kind":{"type":"integer","enum":[1,2],"x-enumNames":["Spam","Abuse"]}}}}}}},
  "delete":{"summary":"Clear a flag","parameters":[{"name":"accountId","in":"query","required":true,"schema":{"type":"integer"}}]}},
 "/notes":{"post":{"summary":"Add a note","requestBody":{"required":true,"content":{"application/json":{"schema":{"type":"object","properties":{"text":{"type":"string"}}}}}}}},
 "/notes/{noteId}":{"patch":{"summary":"Edit a note","parameters":[{"name":"noteId","in":"path","required":true,"schema":{"type":"integer"}}],
   "requestBody":{"content":{"application/json":{"schema":{"type":"object","properties":{"text":{"type":"string"}}}}}}}},
 "/exports":{"post":{"summary":"Start an export"}}
}}`

const (
	findAccount = `{"name": "find_account", "summary": "Find accounts.", "risk": "read", "confident": true,
  "inputs": {"name": {"type": "string"}},
  "steps": [{"id": "s", "operation": "GET /accounts", "parameters": {"name": "$inputs.name"}}]}`
	flagAccount = `{"name": "flag_account", "summary": "Flag an account.", "risk": "write", "confident": true,
  "inputs": {"accountId": {"type": "integer", "required": true}, "kind": {"type": "integer", "required": true}},
  "steps": [{"id": "f", "operation": "POST /flags", "body": {"accountId": "$inputs.accountId", "kind": "$inputs.kind"}}]}`
	// risk read on purpose: a model that calls the undo harmless is corrected.
	clearFlag = `{"name": "clear_flag", "summary": "Clear a flag.", "risk": "read", "confident": true, "undoes": "flag_account",
  "inputs": {"accountId": {"type": "integer", "required": true}},
  "steps": [{"id": "c", "operation": "DELETE /flags", "parameters": {"accountId": "$inputs.accountId"}}]}`
	addNote = `{"name": "add_note", "summary": "Add a note.", "risk": "write", "confident": true,
  "inputs": {"text": {"type": "string", "required": true}},
  "steps": [{"id": "a", "operation": "POST /notes", "body": {"text": "$inputs.text"}}]}`
	startExport = `{"name": "start_export", "summary": "Start an export.", "risk": "write", "confident": true,
  "steps": [{"id": "e", "operation": "POST /exports"}]}`
)

func moderationDoc(t *testing.T) *spec.Document {
	t.Helper()
	d, err := spec.Parse([]byte(moderation))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func run(t *testing.T, a *asker) *Draft {
	t.Helper()
	d, err := Run(context.Background(), moderationDoc(t), a, Options{Title: "moderation", Source: "moderation", URL: "openapi.json"})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func byName(t *testing.T, d *Draft, name string) workflow.Workflow {
	t.Helper()
	for _, w := range d.Document.Workflows {
		if w.WorkflowID == name {
			return w
		}
	}
	t.Fatalf("no tool %s in the draft", name)
	return workflow.Workflow{}
}

func TestAWriteWithAnUndoToolIsLeftAloneAndTheUndoIsAWrite(t *testing.T) {
	a := &asker{answers: []string{answerWith(findAccount, flagAccount, clearFlag)}}
	d := run(t, a)
	if len(a.prompts) != 1 {
		t.Fatalf("%d prompts: nothing is missing, so the model is not asked again", len(a.prompts))
	}
	if len(d.Unreversed) != 0 || byName(t, d, "flag_account").Agent.NeedsReview || byName(t, d, "clear_flag").Agent.NeedsReview {
		t.Fatalf("unreversed %v: both writes undo each other", d.Unreversed)
	}
	if risk := byName(t, d, "clear_flag").Agent.Risk; risk != "write" {
		t.Fatalf("clear_flag risk = %q: taking a write back is not a read", risk)
	}
}

func TestTheModelSeesTheCodesOfABodyField(t *testing.T) {
	if p := prompt(moderationDoc(t)); !strings.Contains(p, "kind{1 = Spam, 2 = Abuse}") || !strings.Contains(p, "undoes") {
		t.Fatalf("prompt lacks the code names or the undo rule:\n%s", p)
	}
}

func TestAWriteWhoseUndoOperationNoToolCallsIsPutToTheModelOnce(t *testing.T) {
	a := &asker{answers: []string{answerWith(findAccount, flagAccount), `{"tools": [` + clearFlag + `]}`}}
	d := run(t, a)
	if len(a.prompts) != 2 || !strings.Contains(a.prompts[1], "flag_account") || !strings.Contains(a.prompts[1], "DELETE /flags") {
		t.Fatalf("second prompt does not name the write and its undo operation:\n%v", a.prompts)
	}
	if len(d.Unreversed) != 0 || len(d.Document.Workflows) != 3 {
		t.Fatalf("unreversed %v, %d tools: the drafted undo should be in", d.Unreversed, len(d.Document.Workflows))
	}
	if risk := byName(t, d, "clear_flag").Agent.Risk; risk != "write" {
		t.Fatalf("clear_flag risk = %q", risk)
	}
}

func TestAWriteTheModelFindsNoUndoForIsLeftForAPersonToDecide(t *testing.T) {
	a := &asker{answers: []string{answerWith(findAccount, flagAccount), `{"tools": []}`}}
	d := run(t, a)
	w := byName(t, d, "flag_account")
	if !w.Agent.NeedsReview || !strings.Contains(w.Agent.ReviewNote, "DELETE /flags") || len(d.Unreversed) != 1 {
		t.Fatalf("needsReview %v, note %q, unreversed %v: the gap must be asked, not skipped", w.Agent.NeedsReview, w.Agent.ReviewNote, d.Unreversed)
	}
	if len(d.Review) != 1 || d.Review[0] != "flag_account" {
		t.Fatalf("review = %v", d.Review)
	}
}

func TestAnUndoDraftedThatDoesNotBindIsDroppedAndTheGapIsAsked(t *testing.T) {
	bad := strings.Replace(clearFlag, "DELETE /flags", "DELETE /flagz", 1)
	a := &asker{answers: []string{answerWith(findAccount, flagAccount), `{"tools": [` + bad + `]}`}}
	d := run(t, a)
	if len(d.Dropped) != 1 || !strings.HasPrefix(d.Dropped[0], "clear_flag: ") || len(d.Unreversed) != 1 {
		t.Fatalf("dropped %v, unreversed %v", d.Dropped, d.Unreversed)
	}
}

func TestAWriteWithNoUndoOperationInTheDocumentIsAskedWithoutAnotherModelCall(t *testing.T) {
	a := &asker{answers: []string{answerWith(findAccount, startExport)}}
	d := run(t, a)
	w := byName(t, d, "start_export")
	if len(a.prompts) != 1 || !w.Agent.NeedsReview || !strings.Contains(w.Agent.ReviewNote, "no operation in the document undoes it") {
		t.Fatalf("prompts %d, needsReview %v, note %q", len(a.prompts), w.Agent.NeedsReview, w.Agent.ReviewNote)
	}
}

func TestAnItemOfTheCollectionAWriteCreatesIntoCountsAsAnUndoCandidate(t *testing.T) {
	a := &asker{answers: []string{answerWith(findAccount, addNote), `{"tools": []}`}}
	d := run(t, a)
	if len(a.prompts) != 2 || !strings.Contains(a.prompts[1], "PATCH /notes/{noteId}") {
		t.Fatalf("prompts %v", a.prompts)
	}
	if note := byName(t, d, "add_note").Agent.ReviewNote; !strings.Contains(note, "PATCH /notes/{noteId}") {
		t.Fatalf("note = %q", note)
	}
}

func TestWhenTheModelCannotBeAskedForAnUndoTheGapIsStillReported(t *testing.T) {
	a := &asker{answers: []string{answerWith(findAccount, flagAccount)}} // the second ask fails: "asked too often"
	d := run(t, a)
	w := byName(t, d, "flag_account")
	if !w.Agent.NeedsReview || !strings.Contains(w.Agent.ReviewNote, "asked too often") {
		t.Fatalf("needsReview %v, note %q: a failed ask must show in the question", w.Agent.NeedsReview, w.Agent.ReviewNote)
	}
}

func TestATaskWrittenToDiskKeepsTheReviewNoteForThePersonOnly(t *testing.T) {
	a := &asker{answers: []string{answerWith(findAccount, startExport)}}
	d := run(t, a)
	data, err := d.Document.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	back, err := workflow.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(back.Workflows[1].Agent.ReviewNote, "no operation") {
		t.Fatalf("note lost on the way to the file:\n%s", data)
	}
}
