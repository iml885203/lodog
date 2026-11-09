package approval

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/iml885203/lodog/internal/policy"
)

func TestTheListingMasksCredentialFieldsAndClipsABigInputButTheStoreKeepsThem(t *testing.T) {
	a := NewStore()
	big := strings.Repeat("QUJD", 2500)
	args := map[string]any{"requestBody": map[string]any{"refNo": "W-1", "password": "S3NTINEL-pw", "attachment": big}}
	if _, err := a.Hold("t", "POST", "/x", "t", policy.Decision{Action: policy.Approval, Risk: "money"}, args, who); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	AdminHandler(a).ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/approvals", nil))
	body := rec.Body.String()
	if strings.Contains(body, "S3NTINEL-pw") || strings.Contains(body, big[:300]) || !strings.Contains(body, "W-1") || len(body) > 4000 {
		t.Fatalf("listing (%d bytes):\n%s", len(body), body)
	}
	// The call that is approved still carries what the agent sent.
	if got := a.Pending()[0].Arguments["requestBody"].(map[string]any)["password"]; got != "S3NTINEL-pw" {
		t.Fatalf("the store changed the arguments: %v", got)
	}
}

func TestAPathCarryingACredentialIsMaskedInTheListingAndEveryNotice(t *testing.T) {
	const path = "/api/x?token=S3NT-path&page=2"
	a := NewStore()
	if _, err := a.Hold("t", "GET", path, "t", policy.Decision{Action: policy.Approval, Risk: "money"}, nil, who); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	AdminHandler(a).ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/approvals", nil))
	if body := rec.Body.String(); strings.Contains(body, "S3NT-path") || !strings.Contains(body, "page=2") {
		t.Errorf("listing:\n%s", body)
	}
	n := NoticeFor(&a.Pending()[0], "x", "")
	if strings.Contains(n.Target, "S3NT-path") || !strings.Contains(n.Target, "page=2") {
		t.Errorf("Target = %q", n.Target)
	}
	// The webhook message and page, and the elicitation prompt, are built
	// from the Notice.
	if m := elicitMessage(n); strings.Contains(m, "S3NT-path") {
		t.Errorf("prompt:\n%s", m)
	}
	if text, blocks := factBlocks(n); strings.Contains(fmt.Sprint(text, blocks), "S3NT-path") {
		t.Errorf("message: %v %v", text, blocks)
	}
}
