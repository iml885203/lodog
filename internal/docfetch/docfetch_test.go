package docfetch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAFileAndAURLGiveTheSameBytes(t *testing.T) {
	const doc = `{"openapi":"3.0.0"}`
	path := filepath.Join(t.TempDir(), "d.json")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(doc)) }))
	defer srv.Close()
	for name, src := range map[string]string{"file": path, "url": srv.URL + "/d.json"} {
		b, err := Fetch(context.Background(), nil, src)
		if err != nil || string(b) != doc {
			t.Errorf("%s: %q, %v", name, b, err)
		}
	}
}

func TestFailuresSaySoPlainly(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	for name, tc := range map[string]struct{ src, want string }{
		"empty":        {"", "no OpenAPI document given"},
		"missing file": {filepath.Join(t.TempDir(), "nope.json"), "read OpenAPI document"},
		"404":          {srv.URL + "/x.json", "404"},
	} {
		if _, err := Fetch(context.Background(), nil, tc.src); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
}

func TestADocumentOverTheLimitIsRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(make([]byte, MaxBytes+1))
	}))
	defer srv.Close()
	if _, err := Fetch(context.Background(), nil, srv.URL); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("err = %v", err)
	}
}
