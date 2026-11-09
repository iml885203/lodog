package run

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iml885203/lodog/internal/casecfg"
	"github.com/iml885203/lodog/internal/gateway"
)

// The runner prints what Session.Do returns into its log and into findings,
// so a Go URL error carrying ?api_key=<secret> must never come back from it.
func TestARunnerErrorNeverCarriesTheGatewaysCredential(t *testing.T) {
	down := httptest.NewServer(nil)
	upstream := down.URL
	down.Close()
	const key = "SENTINEL-runner-key"
	cfg := &casecfg.Config{
		Upstream: upstream,
		Auth:     casecfg.AuthConfig{Type: casecfg.AuthAPIKey, KeyEnv: "K", Query: "api_key"},
	}
	s, err := NewSession(t.Context(), cfg, func(string) string { return key }, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.Do(t.Context(), "GET /api/x?page=1", "T01", 1, nil)
	if err == nil {
		t.Fatal("want a connection error")
	}
	if strings.Contains(err.Error(), key) || strings.Contains(err.Error(), "api_key=") && !strings.Contains(err.Error(), "api_key=REDACTED") {
		t.Errorf("error leaks the key: %v", err)
	}
}

// A sign-in that fails on the way is printed by the runner too.
func TestARunnerSignInErrorNeverCarriesTheClientSecret(t *testing.T) {
	down := httptest.NewServer(nil)
	upstream := down.URL
	down.Close()
	const secret = "SENTINEL-client-secret"
	cfg := &casecfg.Config{
		Upstream: upstream,
		Auth: casecfg.AuthConfig{
			Type: casecfg.AuthOAuth2, TokenURL: upstream + "/token?client_secret=" + secret,
			ClientIDEnv: "ID", ClientSecretEnv: "SECRET", ClientAuth: casecfg.ClientAuthBody,
		},
	}
	env := map[string]string{"ID": "id", "SECRET": secret}
	_, err := NewSession(t.Context(), cfg, func(k string) string { return env[k] }, "run-1")
	if err == nil {
		t.Fatal("want a sign-in error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("error leaks the secret: %v", err)
	}
}

// rawAgent answers with a fixed raw stream, as a tool result carrying a
// password reset link would appear in a real transcript.
type rawAgent struct{ raw string }

func (a rawAgent) Turn(context.Context, string, string) (Turn, error) {
	return Turn{SessionID: "s1", Result: "done", Raw: []byte(a.raw)}, nil
}

func TestATranscriptNeverCarriesWhatAToolReturnedAsACredential(t *testing.T) {
	const link = "https://shop.example?resetPassword=test-reset-token-0000000000000000"
	p := testPlan(t)
	raw := `{"type":"user","message":{"content":[{"type":"tool_result","content":[{"type":"text","text":"HTTP 200\n{\"resetUrl\":\"` + link + `\"}"}]}]}}` + "\n"
	var labels []gateway.Labels
	if err := Run(t.Context(), p, testDeps(rawAgent{raw}, &scriptedJudge{}, &labels)); err != nil {
		t.Fatal(err)
	}
	m := readMeta(t, p.RunDir)
	b, err := os.ReadFile(filepath.Join(p.RunDir, m.Attempts[0].Transcript))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "test-reset-token") || !strings.Contains(string(b), "resetUrl") {
		t.Fatalf("transcript = %s", b)
	}
}
