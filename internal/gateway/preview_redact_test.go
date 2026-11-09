package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/iml885203/lodog/internal/approval"
	"github.com/iml885203/lodog/internal/casecfg"
)

const (
	sentinelA = "S3NTINEL-a-7f3a9c"
	sentinelB = "S3NTINEL-b-b81d42"
)

// A deciding step whose body carries a credential: it is part of the
// workflow, so the agent never sees it, and no record of the held call may.
func credentialBodyDoc() string {
	return strings.Replace(taskDoc, `"amount":{"type":"number"}`, `"amount":{"type":"number"},"password":{"type":"string"},"note":{"type":"string"}`, 1)
}

func credentialBodyWorkflows() string {
	return strings.Replace(taskWorkflows,
		"requestBody: {payload: {amount: $inputs.amount}}",
		"requestBody: {payload: {amount: $inputs.amount, password: "+sentinelA+", note: "+sentinelB+"}}", 1)
}

func TestAHeldTasksStepBodyValuesNeverReachAnyRecordOfTheApproval(t *testing.T) {
	h := newTaskGatewayWith(t, casecfg.L3, bob(), credentialBodyDoc(), credentialBodyWorkflows())
	res, text := h.call(t, "give_bonus", map[string]any{"id": 7, "amount": 100})
	if res.IsError || !strings.HasPrefix(text, "NOT EXECUTED") {
		t.Fatalf("want held, got %v: %s", res.IsError, text)
	}
	pending := h.gw.approvals.Pending()
	if len(pending) != 1 {
		t.Fatalf("pending = %+v", pending)
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, h.admin+"/approvals", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	listing, _ := io.ReadAll(resp.Body)
	for place, got := range map[string]string{
		"needs_approval text": text, "Request.Summary": pending[0].Summary, "GET /approvals": string(listing),
	} {
		for _, secret := range []string{sentinelA, sentinelB} {
			if strings.Contains(got, secret) {
				t.Errorf("%s carries a body value %q:\n%s", place, secret, got)
			}
		}
	}
	// What the person approving can still tell: which fields the step sends.
	if !strings.Contains(pending[0].Summary, "body fields: amount, note, password") {
		t.Errorf("summary = %q, want the body's field names", pending[0].Summary)
	}
}

func TestTheUsersPromptMasksCredentialFieldsAndClipsABigInput(t *testing.T) {
	u := &user{answer: answers("decline")}
	h := newElicit(t, u, nil)
	big := strings.Repeat("QUJD", 2500)
	_, _ = h.call(t, reject, map[string]any{"requestBody": map[string]any{
		"refNo": "BENCH-W-BOB-01", "password": sentinelA, "attachment": big,
	}})
	asked := u.prompts()
	if len(asked) != 1 {
		t.Fatalf("prompts = %d", len(asked))
	}
	msg := asked[0].Message
	if strings.Contains(msg, sentinelA) || strings.Contains(msg, big[:300]) {
		t.Errorf("the prompt shows a credential or the whole blob:\n%s", msg)
	}
	if !strings.Contains(msg, "BENCH-W-BOB-01") || !strings.Contains(msg, "10000 字元") {
		t.Errorf("the prompt lost the ordinary field or the blob's size:\n%s", msg)
	}
}

// credentialArgs puts a sentinel behind every spelling of a credential name
// the mask must know, at the top and nested, and in the values of ordinary
// fields (a URL with user info, a link with a token in its query).
func credentialArgs() map[string]any {
	return map[string]any{"requestBody": map[string]any{
		"refNo": "BENCH-W-BOB-01",
		"pwd":   "S3NT-pwd", "jwt": "S3NT-jwt", "bearer": "S3NT-bearer", "session": "S3NT-session",
		"access_key": "S3NT-accesskey",
		"auth":       map[string]any{"list": []any{map[string]any{"accessKey": "S3NT-nested"}}},
		"callback":   "https://bot:" + "S3NT-userinfo" + "@host.example/x",
		"link":       "https://host.example/x?page=2&token=S3NT-query&api_key=S3NT-apikey&password=S3NT-pw&secret=S3NT-secret&access_token=S3NT-at",
	}}
}

var credentialSentinels = []string{"S3NT-pwd", "S3NT-jwt", "S3NT-bearer", "S3NT-session", "S3NT-accesskey", "S3NT-nested", "S3NT-userinfo", "S3NT-query", "S3NT-apikey", "S3NT-pw", "S3NT-secret", "S3NT-at"}

func TestNoSpellingOfACredentialReachesAnythingAPersonApprovingIsShown(t *testing.T) {
	sent := make(chan string, 4)
	h := newL3Full(t, l3Policy, casecfg.AuthConfig{Type: casecfg.AuthAPIKey, KeyEnv: "K", Header: "X-Key"}, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == rejectPath {
				b, _ := io.ReadAll(r.Body)
				sent <- string(b)
				r.Body = io.NopCloser(strings.NewReader(string(b)))
			}
			next.ServeHTTP(w, r)
		})
	}, nil, nil)
	args := credentialArgs()
	isErr, text := h.call(t, reject, args)
	if isErr || !strings.HasPrefix(text, "NOT EXECUTED") {
		t.Fatalf("want held, got %v: %s", isErr, text)
	}
	pending := h.gw.approvals.Pending()
	if len(pending) != 1 {
		t.Fatalf("pending = %+v", pending)
	}
	resp, err := http.Get(h.admin + "/approvals") //nolint:noctx // test against a local server
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	listing, _ := io.ReadAll(resp.Body)
	for place, got := range map[string]string{
		"needs_approval text": text, "Request.Summary": pending[0].Summary, "GET /approvals": string(listing),
	} {
		for _, secret := range credentialSentinels {
			if strings.Contains(got, secret) {
				t.Errorf("%s shows %q:\n%s", place, secret, got)
			}
		}
	}
	// What is sent upstream once approved is what the agent wrote.
	if h.decide(t, pending[0].ID, "approve") != http.StatusOK {
		t.Fatal("approve failed")
	}
	args["approvalId"] = pending[0].ID
	if isErr, text := h.call(t, reject, args); isErr {
		t.Fatalf("the approved call failed: %s", text)
	}
	var want, got any
	wantJSON, _ := json.Marshal(credentialArgs()["requestBody"])
	_ = json.Unmarshal(wantJSON, &want)
	_ = json.Unmarshal([]byte(<-sent), &got)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("upstream body changed:\n got %v\nwant %v", got, want)
	}
}

func TestTheUsersPromptShowsNoSpellingOfACredential(t *testing.T) {
	u := &user{answer: answers("decline")}
	h := newElicit(t, u, nil)
	_, _ = h.call(t, reject, credentialArgs())
	asked := u.prompts()
	if len(asked) != 1 {
		t.Fatalf("prompts = %d", len(asked))
	}
	for _, secret := range credentialSentinels {
		if strings.Contains(asked[0].Message, secret) {
			t.Errorf("the prompt shows %q:\n%s", secret, asked[0].Message)
		}
	}
	if !strings.Contains(asked[0].Message, "BENCH-W-BOB-01") {
		t.Errorf("the prompt lost the ordinary field:\n%s", asked[0].Message)
	}
}

// A path parameter named like a credential carries the secret in the URL
// itself. A person approving is shown "{name}" for it, and the request that
// goes upstream still has the real value.
func TestACredentialPathParameterIsNotShownToAPersonApproving(t *testing.T) {
	doc := strings.Replace(taskDoc, `"/players/{id}/bonus":{"post":{"parameters":[{"name":"id","in":"path","required":true,"schema":{"type":"integer"}}]`,
		`"/players/{resetToken}/bonus":{"post":{"parameters":[{"name":"resetToken","in":"path","required":true,"schema":{"type":"string"}}]`, 1)
	wf := strings.Replace(taskWorkflows, "~1players~1{id}~1bonus/post'\n        parameters: [{name: id, in: path, value: $inputs.id}]",
		"~1players~1{resetToken}~1bonus/post'\n        parameters: [{name: resetToken, in: path, value: $inputs.token}]", 1)
	wf = strings.Replace(wf, "properties: {id: {type: integer}, amount: {type: number}}", "properties: {id: {type: integer}, token: {type: string}, amount: {type: number}}", 1)
	if doc == taskDoc || wf == taskWorkflows {
		t.Fatal("the fixture did not change")
	}
	legacy := &shop{answers: map[string]answer{
		"GET /players/7":                        {status: 200, body: `{}`},
		"POST /players/" + sentinelA + "/bonus": {status: 200, body: `{}`},
	}}
	h := newTaskGatewayWith(t, casecfg.L3, legacy, doc, wf)
	args := map[string]any{"id": 7, "token": sentinelA, "amount": 100}
	res, text := h.call(t, "give_bonus", args)
	if res.IsError || !strings.HasPrefix(text, "NOT EXECUTED") {
		t.Fatalf("want held, got %v: %s", res.IsError, text)
	}
	pending := h.gw.approvals.Pending()
	if len(pending) != 1 {
		t.Fatalf("pending = %+v", pending)
	}
	resp, err := http.Get(h.admin + "/approvals") //nolint:noctx // test against a local server
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	listing, _ := io.ReadAll(resp.Body)
	notice := approval.NoticeFor(&pending[0], "x", "")
	shown := map[string]string{
		"needs_approval text": text, "Request.Summary": pending[0].Summary, "Request.Path": pending[0].Path,
		"GET /approvals": string(listing), "Notice.Target": notice.Target,
	}
	for place, got := range shown {
		if strings.Contains(got, sentinelA) {
			t.Errorf("%s shows the path parameter's value:\n%s", place, got)
		}
	}
	if notice.Target != "/players/{resetToken}/bonus" {
		t.Errorf("Notice.Target = %q, want the parameter's name in place of its value", notice.Target)
	}
	if _, err := h.gw.approvals.Decide(pending[0].ID, true, "user:lead"); err != nil {
		t.Fatal(err)
	}
	args["approvalId"] = pending[0].ID
	if res, text := h.call(t, "give_bonus", args); res.IsError {
		t.Fatalf("the approved call failed: %s", text)
	}
	if n := len(legacy.reached("POST", "/players/"+sentinelA+"/bonus")); n != 1 {
		t.Errorf("upstream got the real path %d times, want 1", n)
	}
}
