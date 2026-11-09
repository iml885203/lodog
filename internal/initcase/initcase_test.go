package initcase

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iml885203/lodog/internal/casecfg"

	"github.com/iml885203/lodog/internal/bench/draft"
	"github.com/iml885203/lodog/internal/bench/score"
	"github.com/iml885203/lodog/internal/gateway"
	"github.com/iml885203/lodog/internal/spec"
)

const fixture = "testdata/petshop.json"

func generate(t *testing.T, o Options) string {
	t.Helper()
	o.Dir = filepath.Join(t.TempDir(), "petshop")
	if o.Swagger == "" {
		o.Swagger = fixture
		if o.Upstream == "" {
			o.Upstream = "https://petshop.test/api/v3" // a local file gives init nothing to derive it from
		}
	}
	if err := Generate(context.Background(), o); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return o.Dir
}

// The files a user is handed must be ones lodog accepts: a template edit that
// breaks a field name or the YAML turns this red.
func TestGeneratedGatewayConfigLoadsAndServes(t *testing.T) {
	env := map[string]string{
		"LODOG_UPSTREAM_USER": "u", "LODOG_UPSTREAM_PASSWORD": "p",
		"LODOG_UPSTREAM_TOKEN": "t", "LODOG_UPSTREAM_API_KEY": "k",
		"LODOG_UPSTREAM_CLIENT_ID": "id", "LODOG_UPSTREAM_CLIENT_SECRET": "secret",
	}
	for _, auth := range authOrder {
		t.Run(auth, func(t *testing.T) {
			dir := generate(t, Options{Auth: auth})
			cfg, err := casecfg.LoadConfig(filepath.Join(dir, "gateway", "L0.yaml"))
			if err != nil {
				t.Fatalf("LoadConfig: %v", err)
			}
			if cfg.Case != dir || cfg.Level != casecfg.L0 || cfg.Auth.Type != auth {
				t.Errorf("config = case %q level %q auth %q", cfg.Case, cfg.Level, cfg.Auth.Type)
			}
			if cfg.Auth.LoginFormat != "" && cfg.Auth.LoginFormat != casecfg.LoginFormatJSON {
				t.Errorf("login_format = %q, want json", cfg.Auth.LoginFormat)
			}
			if cfg.Upstream != "https://petshop.test/api/v3" {
				t.Errorf("upstream = %q, want the one passed", cfg.Upstream)
			}
			log := slog.New(slog.NewTextHandler(io.Discard, nil))
			getenv := func(k string) string { return env[k] }
			// As generated, the placeholders are still there: serve must say so.
			if _, err := gateway.New(cfg, getenv, gateway.Labels{}, "test", log); err == nil ||
				!strings.Contains(err.Error(), "identity.on_behalf_of") {
				t.Fatalf("unfilled config: err = %v, want the placeholder named", err)
			}
			path := filepath.Join(dir, "gateway", "L0.yaml")
			text, err := os.ReadFile(path) //nolint:gosec // G304: a path inside the test's own temp dir
			if err != nil {
				t.Fatal(err)
			}
			filled := strings.ReplaceAll(string(text), casecfg.Placeholder, "real")
			if err := os.WriteFile(path, []byte(filled), 0o600); err != nil { //nolint:gosec // G703: a path inside the test's own temp dir
				t.Fatal(err)
			}
			if cfg, err = casecfg.LoadConfig(path); err != nil {
				t.Fatal(err)
			}
			if _, err := gateway.New(cfg, getenv, gateway.Labels{}, "test", log); err != nil {
				t.Fatalf("gateway.New after filling the placeholders: %v", err)
			}
		})
	}
}

func TestGeneratedSpecIsTheDocumentGiven(t *testing.T) {
	dir := generate(t, Options{})
	doc, err := spec.LoadFile(filepath.Join(dir, "spec", "openapi.json"))
	if err != nil {
		t.Fatalf("spec.LoadFile: %v", err)
	}
	if doc.Title != "Petshop" || len(doc.Operations) != 3 {
		t.Errorf("got %q with %d operations, want Petshop with 3", doc.Title, len(doc.Operations))
	}
}

// Whatever form the source is in, the folder holds OpenAPI 3.x JSON, so the
// gateway and every other command read one format.
func TestSwagger2AndYAMLSourcesAreStoredAsOpenAPI3JSON(t *testing.T) {
	for name, src := range map[string]string{
		"swagger 2.0 yaml": "../spec/testdata/swagger2-features.yaml",
		"openapi 3.0 yaml": "../spec/testdata/petstore-openapi3.yaml",
	} {
		t.Run(name, func(t *testing.T) {
			dir := generate(t, Options{Swagger: src})
			stored, err := os.ReadFile(filepath.Join(dir, "spec", "openapi.json")) //nolint:gosec // G304: a path inside the test's own temp dir
			if err != nil {
				t.Fatal(err)
			}
			var head struct {
				OpenAPI string `json:"openapi"`
				Swagger string `json:"swagger"`
			}
			if err := json.Unmarshal(stored, &head); err != nil {
				t.Fatalf("stored spec is not JSON: %v", err)
			}
			if !strings.HasPrefix(head.OpenAPI, "3.") || head.Swagger != "" {
				t.Fatalf("stored spec declares openapi=%q swagger=%q, want 3.x only", head.OpenAPI, head.Swagger)
			}
		})
	}
}

func TestDefaultFilesOnly(t *testing.T) {
	dir := generate(t, Options{})
	for _, f := range []string{"case.yaml", "spec/openapi.json", "gateway/L0.yaml", "gateway/L1.yaml", "gateway/L2.yaml", "gateway/L3.yaml", "README.md"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("missing %s", f)
		}
	}
	for _, f := range []string{"tasks.md", "expected/answers.yaml", "rubric.yaml"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			t.Errorf("%s written without --with-bench", f)
		}
	}
}

func TestWithBenchWritesATaskBankLodogCanLoad(t *testing.T) {
	dir := generate(t, Options{WithBench: true})
	c, err := score.LoadCase(dir)
	if err != nil {
		t.Fatalf("score.LoadCase: %v", err)
	}
	task, ok := c.Tasks["T01"]
	if !ok || len(c.Tasks) != 1 {
		t.Fatalf("tasks = %v, want exactly the T01 example", c.Tasks)
	}
	if task.Calls[0] != "GET /store/inventory" {
		t.Errorf("example call = %q, want the first GET without a path parameter", task.Calls[0])
	}
	doc, err := spec.LoadFile(c.Spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range draft.Check(c, doc) {
		if f.Severity == draft.SeverityError {
			t.Errorf("bench check: %s %s", f.Task, f.Message)
		}
	}
}

func TestExistingTargetIsRefusedAndLeftAlone(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "petshop")
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	mine := filepath.Join(dir, "case.yaml")
	if err := os.WriteFile(mine, []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := Generate(context.Background(), Options{Dir: dir, Swagger: fixture, Auth: casecfg.AuthBearer})
	if !errors.Is(err, ErrExists) {
		t.Fatalf("err = %v, want ErrExists", err)
	}
	if b, _ := os.ReadFile(filepath.Clean(mine)); string(b) != "mine" {
		t.Errorf("existing file overwritten: %q", b)
	}
}

func TestBadInputCreatesNothing(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "notjson.yaml")
	if err := os.WriteFile(bad, []byte("openapi: 3.0.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, o := range map[string]Options{
		"not json":        {Swagger: bad, Auth: casecfg.AuthBearer},
		"missing file":    {Swagger: filepath.Join(t.TempDir(), "nope.json"), Auth: casecfg.AuthBearer},
		"unknown auth":    {Swagger: fixture, Auth: "oauth"},
		"no swagger flag": {Auth: casecfg.AuthBearer},
	} {
		t.Run(name, func(t *testing.T) {
			o.Dir = filepath.Join(t.TempDir(), "petshop")
			if err := Generate(context.Background(), o); err == nil {
				t.Fatal("accepted")
			}
			if _, err := os.Stat(o.Dir); err == nil {
				t.Errorf("%s left behind", o.Dir)
			}
		})
	}
}

func TestFetchesAURLAndDerivesTheUpstreamFromServers(t *testing.T) {
	doc, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/docs/openapi.json" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(doc)
	}))
	defer srv.Close()
	dir := generate(t, Options{Swagger: srv.URL + "/docs/openapi.json", Auth: casecfg.AuthAPIKey})
	cfg, err := casecfg.LoadConfig(filepath.Join(dir, "gateway", "L0.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if want := srv.URL + "/api/v3"; cfg.Upstream != want {
		t.Errorf("upstream = %q, want %q (the relative servers entry resolved against the swagger URL)", cfg.Upstream, want)
	}
}

func TestFetchFailureIsReported(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	err := Generate(context.Background(), Options{Dir: filepath.Join(t.TempDir(), "x"), Swagger: srv.URL + "/missing.json", Auth: casecfg.AuthBearer})
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("err = %v, want a 404", err)
	}
}

func TestUpstreamFlagWins(t *testing.T) {
	dir := generate(t, Options{Upstream: "https://example.test/base/"})
	cfg, err := casecfg.LoadConfig(filepath.Join(dir, "gateway", "L0.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Upstream != "https://example.test/base" {
		t.Errorf("upstream = %q", cfg.Upstream)
	}
}

func TestEveryAuthTypeShowsTheOthersAsComments(t *testing.T) {
	dir := generate(t, Options{Auth: casecfg.AuthBearer})
	b, err := os.ReadFile(filepath.Clean(filepath.Join(dir, "gateway", "L0.yaml")))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# Instead, for a system that uses cookie-session", "# Instead, for a system that uses api-key", "# Instead, for a system that uses oauth2-client-credentials"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("L0.yaml lacks %q", want)
		}
	}
}

// The oauth2 block names every field a user must fill and says where the
// credentials come from, so the first run does not start by reading contracts.md.
func TestTheOAuth2TemplateNamesWhatTheUserMustFill(t *testing.T) {
	dir := generate(t, Options{Auth: casecfg.AuthOAuth2})
	cfg, err := casecfg.LoadConfig(filepath.Join(dir, "gateway", "L0.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	a := cfg.Auth
	if a.TokenURL == "" || a.ClientIDEnv != "LODOG_UPSTREAM_CLIENT_ID" || a.ClientSecretEnv != "LODOG_UPSTREAM_CLIENT_SECRET" {
		t.Errorf("auth = %+v, want token_url, client_id_env and client_secret_env set", a)
	}
	b, err := os.ReadFile(filepath.Clean(filepath.Join(dir, "gateway", "L0.yaml")))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "# scope:") {
		t.Error("L0.yaml lacks a commented scope line")
	}
	readme, err := os.ReadFile(filepath.Clean(filepath.Join(dir, "README.md")))
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"LODOG_UPSTREAM_CLIENT_ID", "LODOG_UPSTREAM_CLIENT_SECRET"} {
		if !strings.Contains(string(readme), v) {
			t.Errorf("README.md does not tell the user to export %s", v)
		}
	}
}

// Every command the generated README tells the user to run must be one the CLI
// accepts: lodog score takes a gateway config with -c, not a case folder.
func TestTheReadmeScoreCommandUsesAFlagScoreAccepts(t *testing.T) {
	dir := generate(t, Options{})
	readme, err := os.ReadFile(filepath.Clean(filepath.Join(dir, "README.md")))
	if err != nil {
		t.Fatal(err)
	}
	want := "lodog score -c " + dir + "/gateway/L0.yaml"
	if !strings.Contains(string(readme), want) {
		t.Errorf("README.md lacks %q", want)
	}
}

func TestInitNamesTheOperationsTheGatewayWillNotServe(t *testing.T) {
	var out strings.Builder
	generate(t, Options{Swagger: "testdata/petstore3.json", Upstream: "https://petstore.test/api/v3", Out: &out})
	if !strings.Contains(out.String(), "not served: 1 operation(s)") ||
		!strings.Contains(out.String(), "POST /pet/{petId}/uploadImage: application/octet-stream bodies are not forwarded") {
		t.Fatalf("init output does not list the skipped upload:\n%s", out.String())
	}
}

// tlsDoc serves the petshop document over a self-signed certificate, as a
// local legacy system does.
func tlsDoc(t *testing.T) *httptest.Server {
	t.Helper()
	doc, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(doc) }))
	t.Cleanup(srv.Close)
	return srv
}

func TestInsecureFetchesASelfSignedDocumentAndEveryLevelTrustsTheUpstream(t *testing.T) {
	srv := tlsDoc(t)
	dir := generate(t, Options{Swagger: srv.URL + "/openapi.json", Upstream: srv.URL + "/api", Insecure: true})
	for _, level := range casecfg.Levels() {
		cfg, err := casecfg.LoadConfig(filepath.Join(dir, "gateway", string(level)+".yaml"))
		if err != nil {
			t.Fatal(err)
		}
		if !cfg.UpstreamInsecureTLS {
			t.Errorf("%s: upstream_insecure_tls = false, want true", level)
		}
	}
}

func TestWithoutInsecureASelfSignedDocumentIsRefusedAndTheErrorNamesTheFlag(t *testing.T) {
	srv := tlsDoc(t)
	dir := filepath.Join(t.TempDir(), "petshop")
	err := Generate(context.Background(), Options{Dir: dir, Swagger: srv.URL + "/openapi.json"})
	if err == nil || !strings.Contains(err.Error(), "--insecure") {
		t.Fatalf("err = %v, want it to suggest --insecure", err)
	}
	if _, statErr := os.Lstat(dir); statErr == nil {
		t.Error("a failed init left a folder behind")
	}
	cfgDir := generate(t, Options{})
	cfg, err := casecfg.LoadConfig(filepath.Join(cfgDir, "gateway", "L0.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UpstreamInsecureTLS {
		t.Error("upstream_insecure_tls is true without --insecure")
	}
}
