package gateway

import (
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/iml885203/lodog/internal/serving"

	"github.com/iml885203/lodog/internal/casecfg"
)

const mixedBodies = `{"openapi":"3.0.3","paths":{
  "/xml":{"post":{"operationId":"postXML","requestBody":{"content":{"application/xml":{"schema":{"type":"object"}}}}}},
  "/raw":{"post":{"operationId":"postRaw","requestBody":{"content":{"application/octet-stream":{"schema":{"type":"string","format":"binary"}}}}}},
  "/json":{"post":{"operationId":"postJSON","requestBody":{"content":{"application/json":{"schema":{"type":"object"}}}}}}}}`

// The skipped list that score and init print is the one serve warns about.
func TestServeWarnsAboutExactlyWhatPlanSkips(t *testing.T) {
	upstream := httptest.NewServer(&recorder{})
	t.Cleanup(upstream.Close)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "spec"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "spec", "openapi.json"), []byte(mixedBodies), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &casecfg.Config{
		Case: dir, Level: casecfg.L0, Listen: "127.0.0.1:0", Upstream: upstream.URL,
		Auth: casecfg.AuthConfig{Type: casecfg.AuthBearer, TokenEnv: "T"}, Identity: casecfg.IdentityConfig{Actor: "agent:test"},
	}
	logs := &lockedBuffer{}
	if _, err := New(cfg, func(string) string { return "tok" }, Labels{}, "test", slog.New(slog.NewTextHandler(logs, nil))); err != nil {
		t.Fatal(err)
	}
	doc, err := casecfg.LoadDocument(cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, skipped := serving.RawTools(doc)
	var planned, warned []string
	for _, s := range skipped {
		planned = append(planned, s.Op.Method+" "+s.Op.Path)
	}
	for _, line := range linesWith(logs.String(), "operation not served") {
		for _, p := range []string{"/xml", "/raw", "/json"} {
			if strings.Contains(line, "path="+p+" ") {
				warned = append(warned, "POST "+p)
			}
		}
	}
	sort.Strings(planned)
	sort.Strings(warned)
	if strings.Join(planned, ",") != "POST /raw,POST /xml" || strings.Join(warned, ",") != strings.Join(planned, ",") {
		t.Fatalf("plan skips %v, serve warns about %v", planned, warned)
	}
}
