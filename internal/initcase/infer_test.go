package initcase

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iml885203/lodog/internal/casecfg"

	"github.com/iml885203/lodog/internal/gateway"
)

// initFrom runs init on a testdata swagger, takes the generated L0 config the
// way a user would (no edits) and returns it.
func initFrom(t *testing.T, swagger string, o Options) (*casecfg.Config, string) {
	t.Helper()
	o.Dir = filepath.Join(t.TempDir(), "sys")
	o.Swagger = swagger
	if err := Generate(context.Background(), o); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	cfg, err := casecfg.LoadConfig(filepath.Join(o.Dir, "gateway", "L0.yaml"))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	cfg.Identity.OnBehalfOf = "user:me" // the one value init cannot know, left as a placeholder
	return cfg, o.Dir
}

// The point of reading securitySchemes: the generated auth works as written.
// Each case is a different real-world declaration, none of them Backoffice.
func TestAuthIsReadFromTheDocumentsSecuritySchemes(t *testing.T) {
	const idpEndpoint = "https://idp.example.test/realms/m/protocol/openid-connect/token"
	for name, tc := range map[string]struct {
		swagger string
		opts    Options
		want    casecfg.AuthConfig // only the fields that come from the document
	}{
		"apiKey in a header (petstore3)": {
			"testdata/petstore3.json",
			Options{Upstream: "https://petstore3.swagger.io/api/v3"},
			casecfg.AuthConfig{Type: casecfg.AuthAPIKey, Header: "api_key"},
		},
		"http bearer": {
			"testdata/bearer.json",
			Options{},
			casecfg.AuthConfig{Type: casecfg.AuthBearer},
		},
		"oauth2 clientCredentials": {
			"testdata/oauth2.json",
			Options{},
			casecfg.AuthConfig{Type: casecfg.AuthOAuth2, TokenURL: idpEndpoint, Scope: "read write"},
		},
		"several schemes: the one the document applies, not the first by name": {
			"testdata/mixed.json",
			Options{},
			casecfg.AuthConfig{Type: casecfg.AuthAPIKey, Query: "token"},
		},
		"no schemes: the form-login guess": {
			"testdata/bare.json",
			Options{},
			casecfg.AuthConfig{Type: casecfg.AuthCookieSession},
		},
	} {
		t.Run(name, func(t *testing.T) {
			cfg, _ := initFrom(t, tc.swagger, tc.opts)
			a, w := cfg.Auth, tc.want
			if a.Type != w.Type || a.Header != w.Header || a.Query != w.Query || a.TokenURL != w.TokenURL || a.Scope != w.Scope {
				t.Errorf("auth = %+v, want type %q header %q query %q token_url %q scope %q", a, w.Type, w.Header, w.Query, w.TokenURL, w.Scope)
			}
		})
	}
}

// What init wrote must also start: secrets are named by env only, and the
// fields it filled in are ones the gateway accepts.
func TestTheInferredAuthStartsAGateway(t *testing.T) {
	for _, swagger := range []string{"testdata/petstore3.json", "testdata/bearer.json", "testdata/oauth2.json", "testdata/mixed.json", "testdata/bare.json"} {
		t.Run(swagger, func(t *testing.T) {
			cfg, _ := initFrom(t, swagger, Options{Upstream: "https://x.example.test"})
			if strings.Contains(cfg.Auth.Login, casecfg.Placeholder) {
				cfg.Auth.Login = "POST /login" // a guess has no login call to read from the document
			}
			env := map[string]string{}
			for _, k := range []string{cfg.Auth.KeyEnv, cfg.Auth.TokenEnv, cfg.Auth.ClientIDEnv, cfg.Auth.ClientSecretEnv, cfg.Auth.UsernameEnv, cfg.Auth.PasswordEnv} {
				env[k] = "x"
			}
			log := slog.New(slog.NewTextHandler(io.Discard, nil))
			if _, err := gateway.New(cfg, func(k string) string { return env[k] }, gateway.Labels{}, "test", log); err != nil {
				t.Errorf("gateway.New: %v", err)
			}
		})
	}
}

func TestASwagger2KeyDefinitionIsReadToo(t *testing.T) {
	doc := `{"swagger":"2.0","info":{"title":"Old","version":"1"},"host":"old.example.test","basePath":"/api","schemes":["https"],
		"securityDefinitions":{"k":{"type":"apiKey","in":"header","name":"X-Old-Key"}},
		"paths":{"/a":{"get":{"responses":{"200":{"description":"ok"}}}}}}`
	path := filepath.Join(t.TempDir(), "old.json")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, _ := initFrom(t, path, Options{})
	if cfg.Auth.Type != casecfg.AuthAPIKey || cfg.Auth.Header != "X-Old-Key" || cfg.Upstream != "https://old.example.test/api" {
		t.Fatalf("auth = %+v, upstream = %q", cfg.Auth, cfg.Upstream)
	}
}

func TestAnExplicitAuthWinsButStillTakesTheDocumentsFields(t *testing.T) {
	cfg, _ := initFrom(t, "testdata/mixed.json", Options{Auth: casecfg.AuthBearer})
	if cfg.Auth.Type != casecfg.AuthBearer {
		t.Fatalf("type = %q, --auth must win", cfg.Auth.Type)
	}
	cfg, _ = initFrom(t, "testdata/oauth2.json", Options{Auth: casecfg.AuthAPIKey})
	if cfg.Auth.Type != casecfg.AuthAPIKey || cfg.Auth.Header == "" {
		t.Fatalf("auth = %+v: asking for a type the document does not declare still gives a working block", cfg.Auth)
	}
}

func TestSchemesNotUsedAreListedAsComments(t *testing.T) {
	_, dir := initFrom(t, "testdata/mixed.json", Options{})
	b, err := os.ReadFile(filepath.Clean(filepath.Join(dir, "gateway", "L0.yaml")))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"read from securitySchemes.key",
		"securitySchemes.alpha (apiKey in header X-Alpha), usable as auth.type api-key",
		"securitySchemes.basic (http basic), not usable",
		"securitySchemes.sso (oauth2 authorizationCode), not usable",
	} {
		if !strings.Contains(string(b), want) {
			t.Errorf("L0.yaml lacks %q", want)
		}
	}
}

func TestWithoutAnUpstreamInitFailsAndWritesNothing(t *testing.T) {
	// A local file whose servers entry is relative gives nothing to derive it from.
	o := Options{Dir: filepath.Join(t.TempDir(), "sys"), Swagger: "testdata/petstore3.json"}
	err := Generate(context.Background(), o)
	if err == nil || !strings.Contains(err.Error(), "--upstream") {
		t.Fatalf("err = %v, want a message that says how to pass --upstream", err)
	}
	if _, statErr := os.Stat(o.Dir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("init left %s behind after failing", o.Dir)
	}
}

// assertL3LoadsOnceItHasTaskTools: an L3 serves L2's task tools, so it names
// lodog workflows draft until the case has them.
func assertL3LoadsOnceItHasTaskTools(t *testing.T, dir string, cfg *casecfg.Config) {
	t.Helper()
	env := func(string) string { return "x" }
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := gateway.New(cfg, env, gateway.Labels{}, "test", log); err == nil || !strings.Contains(err.Error(), "lodog workflows draft") {
		t.Errorf("L3 with no task tools: err = %v, want it to name lodog workflows draft", err)
	}
	workflows := `arazzo: 1.0.1
info: {title: t, version: "1", description: list_things lists the things.}
sourceDescriptions: [{name: api, url: openapi.json, type: openapi}]
workflows:
  - workflowId: list_things
    summary: List the things.
    steps:
      - stepId: list
        operationPath: '{$sourceDescriptions.api.url}#/paths/~1things/get'
`
	if err := os.WriteFile(filepath.Join(dir, "spec", "workflows.arazzo.yaml"), []byte(workflows), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := gateway.New(cfg, env, gateway.Labels{}, "test", log); err != nil {
		t.Errorf("L3 with overlay, policy and task tools: %v", err)
	}
}

func TestEveryLevelConfigLoadsAndTheMissingFilesAreNamed(t *testing.T) {
	_, dir := initFrom(t, "testdata/bearer.json", Options{})
	env := func(string) string { return "x" }
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	load := func(level string) *casecfg.Config {
		cfg, err := casecfg.LoadConfig(filepath.Join(dir, "gateway", level+".yaml"))
		if err != nil {
			t.Fatalf("%s: %v", level, err)
		}
		cfg.Identity.OnBehalfOf = "user:me"
		return cfg
	}
	// Before the overlay or policy exists the gateway says what to run.
	for level, want := range map[string]string{"L1": "lodog enrich", "L3": "lodog enrich"} {
		if _, err := gateway.New(load(level), env, gateway.Labels{}, "test", log); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want it to name %q", level, err, want)
		}
	}
	overlay := "overlay: 1.0.0\ninfo: {title: t, version: '1'}\nactions: []\n"
	if err := os.WriteFile(filepath.Join(dir, "spec", "overlay.yaml"), []byte(overlay), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := gateway.New(load("L1"), env, gateway.Labels{}, "test", log); err != nil {
		t.Errorf("L1 with an overlay: %v", err)
	}
	if _, err := gateway.New(load("L3"), env, gateway.Labels{}, "test", log); err == nil || !strings.Contains(err.Error(), "lodog policy draft") {
		t.Errorf("L3 with no policy: err = %v, want it to name lodog policy draft", err)
	}
	policy := "risks:\n  write: approval\ndefault: write\nrules: []\n"
	if err := os.WriteFile(filepath.Join(dir, "spec", "policy.yaml"), []byte(policy), 0o600); err != nil {
		t.Fatal(err)
	}
	assertL3LoadsOnceItHasTaskTools(t, dir, load("L3"))
	l1, _ := casecfg.LoadConfig(filepath.Join(dir, "gateway", "L1.yaml"))
	l0, _ := casecfg.LoadConfig(filepath.Join(dir, "gateway", "L0.yaml"))
	if l1.Auth != l0.Auth || l1.Upstream != l0.Upstream {
		t.Errorf("L1 and L0 differ in auth or upstream: %+v vs %+v", l1.Auth, l0.Auth)
	}
}
