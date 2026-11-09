package cli

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iml885203/lodog/internal/casecfg"
)

// The flag is what the owner types on stage: it must reach the fetch and the
// written configs.
func TestInitInsecureFlagReachesTheFetchAndTheConfig(t *testing.T) {
	doc, err := os.ReadFile("../initcase/testdata/petshop.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(doc) }))
	defer srv.Close()
	run := func(extra ...string) (string, error) {
		dir := filepath.Join(t.TempDir(), "petshop")
		root := newRootCmd()
		root.SetOut(&bytes.Buffer{})
		root.SetErr(&bytes.Buffer{})
		root.SetArgs(append([]string{"init", dir, "--swagger", srv.URL + "/openapi.json", "--upstream", srv.URL + "/api"}, extra...))
		return dir, root.Execute()
	}
	if _, err := run(); err == nil || !strings.Contains(err.Error(), "--insecure") {
		t.Fatalf("without the flag: err = %v, want a hint to use --insecure", err)
	}
	dir, err := run("--insecure")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := casecfg.LoadConfig(filepath.Join(dir, "gateway", "L3.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.UpstreamInsecureTLS {
		t.Error("L3 config lacks upstream_insecure_tls: true")
	}
}

func TestScoreInsecureFetchesASelfSignedDocument(t *testing.T) {
	data, err := os.ReadFile("../readiness/testdata/petstore.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(data) }))
	defer srv.Close()
	run := func(extra ...string) error {
		root := newRootCmd()
		root.SetOut(&bytes.Buffer{})
		root.SetErr(&bytes.Buffer{})
		root.SetArgs(append([]string{"score", srv.URL + "/openapi.json", "--json"}, extra...))
		return root.Execute()
	}
	if err := run(); err == nil || !strings.Contains(err.Error(), "--insecure") {
		t.Fatalf("without the flag: err = %v, want a hint to use --insecure", err)
	}
	if err := run("--insecure"); err != nil {
		t.Fatal(err)
	}
}
