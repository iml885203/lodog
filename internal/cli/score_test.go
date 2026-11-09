package cli

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// A swagger URL is what a new user usually has; scoring it must give the same
// report as scoring the same document from a file.
func TestScoreTakesAURLAndGivesTheFilesReport(t *testing.T) {
	const doc = "../readiness/testdata/petstore.json"
	data, err := os.ReadFile(doc)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v3/openapi.json" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(data)
	}))
	defer srv.Close()

	run := func(src string) string {
		var out bytes.Buffer
		root := newRootCmd()
		root.SetOut(&out)
		root.SetArgs([]string{"score", src, "--json"})
		if err := root.Execute(); err != nil {
			t.Fatalf("score %s: %v", src, err)
		}
		return out.String()
	}
	url := srv.URL + "/v3/openapi.json"
	fromFile, fromURL := run(doc), strings.ReplaceAll(run(url), url, doc)
	if fromURL != fromFile || !strings.Contains(fromFile, "\"points\"") {
		t.Fatalf("URL report differs from the file report:\n%s\n---\n%s", fromURL, fromFile)
	}
}

func TestScoreSaysWhenTheURLCannotBeFetched(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	root := newRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"score", srv.URL + "/missing.json"})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("err = %v, want it to say 404", err)
	}
}
