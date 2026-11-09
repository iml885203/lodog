// Package initcase writes the files a team needs to put its own system behind
// lodog: the case description, the system's OpenAPI document, one gateway
// config per level the gateway serves (casecfg.Levels) and a README, plus a
// starter task bank on request. It touches nothing outside the folder it is
// asked to create.
package initcase

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"github.com/iml885203/lodog/internal/serving"

	"github.com/iml885203/lodog/internal/casecfg"

	"github.com/iml885203/lodog/internal/docfetch"
	"github.com/iml885203/lodog/internal/spec"
)

//go:embed templates/*
var templates embed.FS

// ErrExists is returned when the target folder already exists: init never
// writes into, or over, a user's own files.
var ErrExists = errors.New("target already exists")

// Options say what to generate.
type Options struct {
	// Dir is the folder to create. Its last element names the system, and it is
	// written into the gateway config as given, so a relative Dir is relative to
	// where `lodog serve` is run.
	Dir string
	// Swagger is the OpenAPI document: an http(s) URL or a file path.
	Swagger string
	// Upstream overrides the system's base URL, otherwise taken from the
	// document's servers entry. Generate fails when neither gives one.
	Upstream string
	// Auth is the gateway.Auth* type written as the active auth block. Empty
	// means: read it from the document's securitySchemes.
	Auth string
	// Out receives a short account of what was inferred; nil discards it.
	Out io.Writer
	// WithBench adds the starter task bank.
	WithBench bool
	// Client fetches a Swagger URL; nil means a client with a timeout.
	Client *http.Client
	// Insecure accepts a self-signed certificate: it skips TLS verification
	// when fetching the Swagger URL (when Client is nil) and writes
	// upstream_insecure_tls: true into every gateway config.
	Insecure bool
}

// Generate writes the case folder. It fetches and parses the OpenAPI document
// and renders every file before it creates anything, so a failure leaves no
// half-made folder behind.
func Generate(ctx context.Context, o Options) error {
	dir := filepath.Clean(o.Dir)
	name := filepath.Base(dir)
	if name == "." || name == string(filepath.Separator) || name == ".." {
		return fmt.Errorf("%q does not name a system", o.Dir)
	}
	if _, err := os.Lstat(dir); err == nil {
		return fmt.Errorf("%w: %s (init never overwrites your files)", ErrExists, dir)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("check %s: %w", dir, err)
	}
	if _, ok := authSnippets[o.Auth]; !ok && o.Auth != "" {
		return fmt.Errorf("auth %q is not one of %s", o.Auth, strings.Join(authOrder, ", "))
	}

	raw, doc, err := readDocument(ctx, o)
	if err != nil {
		return err
	}
	pretty, err := indent(raw)
	if err != nil {
		return err
	}
	up, err := upstream(o, raw)
	if err != nil {
		return err
	}
	authType, hint, notes := inferAuth(readSchemes(raw), o.Auth)
	snippet := snippetFor(authType, hint)

	data := templateData{
		Name:        name,
		Dir:         filepath.ToSlash(dir),
		Swagger:     o.Swagger,
		Upstream:    up,
		Auth:        authType,
		Login:       snippet.login,
		AuthBlock:   authBlock(authType, hint, notes),
		EnvHelp:     envHelp(snippet),
		WithBench:   o.WithBench,
		Insecure:    o.Insecure,
		ExampleGet:  exampleGet(doc),
		Placeholder: casecfg.Placeholder,
	}
	files, err := renderFiles(data, o.WithBench)
	if err != nil {
		return err
	}
	files[casecfg.OpenAPIFile] = pretty
	if err := write(dir, files); err != nil {
		return err
	}
	if o.Out != nil {
		_, _ = fmt.Fprintf(o.Out, "upstream: %s\n", up)
		for _, n := range notes[:1] {
			_, _ = fmt.Fprintf(o.Out, "%s\n", n)
		}
		reportSkipped(o.Out, doc)
	}
	return nil
}

// reportSkipped names every operation the gateway will not serve, so the
// team learns what is missing now rather than from lodog serve's warnings.
func reportSkipped(w io.Writer, doc *spec.Document) {
	_, skipped := serving.RawTools(doc)
	if len(skipped) == 0 {
		return
	}
	_, _ = fmt.Fprintf(w, "not served: %d operation(s) will not become tools\n", len(skipped))
	for _, s := range skipped {
		_, _ = fmt.Fprintf(w, "  %s %s: %s\n", s.Op.Method, s.Op.Path, s.Reason)
	}
}

// readDocument fetches the OpenAPI document and returns it normalized to
// OpenAPI 3.x JSON, with its parsed form.
func readDocument(ctx context.Context, o Options) ([]byte, *spec.Document, error) {
	client := o.Client
	if client == nil && o.Insecure {
		client = docfetch.InsecureClient()
	}
	raw, err := docfetch.Fetch(ctx, client, o.Swagger)
	if err != nil {
		return nil, nil, err
	}
	// The folder always holds OpenAPI 3.x JSON, whatever form the source is in.
	raw, err = spec.Normalize(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("%s is not a Swagger 2.0 or OpenAPI 3.x document lodog can read: %w", o.Swagger, err)
	}
	doc, err := spec.Parse(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("%s is not a document lodog can read: %w", o.Swagger, err)
	}
	if len(doc.Operations) == 0 {
		return nil, nil, fmt.Errorf("%s has no operations", o.Swagger)
	}
	return raw, doc, nil
}

// renderFiles renders every template-made file: one gateway config per level
// the gateway serves, the case description, the README and, on request, the
// starter task bank.
func renderFiles(data templateData, withBench bool) (map[string][]byte, error) {
	files := map[string][]byte{}
	plan := []struct{ out, tmpl string }{
		{"case.yaml", "case.yaml.tmpl"},
		{"README.md", "README.md.tmpl"},
	}
	if withBench {
		plan = append(plan,
			struct{ out, tmpl string }{"tasks.md", "tasks.md.tmpl"},
			struct{ out, tmpl string }{"expected/answers.yaml", "answers.yaml.tmpl"},
			struct{ out, tmpl string }{"rubric.yaml", "rubric.yaml.tmpl"},
		)
	}
	for _, p := range plan {
		b, err := render(p.tmpl, data)
		if err != nil {
			return nil, err
		}
		files[p.out] = b
	}
	for _, level := range casecfg.Levels() {
		d := data
		d.Level = string(level)
		b, err := render("gateway.yaml.tmpl", d)
		if err != nil {
			return nil, err
		}
		files["gateway/"+d.Level+".yaml"] = b
	}
	return files, nil
}

type templateData struct {
	Level                                     string
	Name, Dir, Swagger, Upstream, Auth, Login string
	AuthBlock, EnvHelp, ExampleGet            string
	Placeholder                               string
	WithBench, Insecure                       bool
}

func render(name string, data templateData) ([]byte, error) {
	t, err := template.ParseFS(templates, "templates/"+name)
	if err != nil {
		return nil, fmt.Errorf("parse template %s: %w", name, err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("render %s: %w", name, err)
	}
	return buf.Bytes(), nil
}

// write creates dir and every file with O_EXCL, so a race with another
// process cannot make init overwrite something that appeared meanwhile.
func write(dir string, files map[string][]byte) error {
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	if err := os.MkdirAll(filepath.Dir(dir), 0o750); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(dir), err)
	}
	if err := os.Mkdir(dir, 0o750); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("%w: %s", ErrExists, dir)
		}
		return fmt.Errorf("create %s: %w", dir, err)
	}
	for _, n := range names {
		path := filepath.Join(dir, filepath.FromSlash(n))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // G304: path is under the folder the operator named
		if err != nil {
			return fmt.Errorf("create %s: %w", path, err)
		}
		_, werr := f.Write(files[n])
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return fmt.Errorf("write %s: %w", path, werr)
		}
	}
	return nil
}

func indent(raw []byte) ([]byte, error) {
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return nil, fmt.Errorf("format openapi document: %w", err)
	}
	buf.WriteByte('\n')
	return buf.Bytes(), nil
}

// upstream is the system's base URL: the flag, else the document's first
// servers entry (resolved against the Swagger URL when it is relative), else
// the Swagger URL's own origin. When none of them gives one it fails rather
// than write a placeholder that looks usable.
func upstream(o Options, raw []byte) (string, error) {
	if o.Upstream != "" {
		return strings.TrimRight(o.Upstream, "/"), nil
	}
	var d struct {
		Servers []struct {
			URL string `json:"url"`
		} `json:"servers"`
	}
	_ = json.Unmarshal(raw, &d)
	src, srcErr := url.Parse(o.Swagger)
	remote := srcErr == nil && (src.Scheme == "http" || src.Scheme == "https")
	if len(d.Servers) > 0 {
		if s, err := url.Parse(d.Servers[0].URL); err == nil && !strings.Contains(d.Servers[0].URL, "{") {
			if s.IsAbs() {
				return strings.TrimRight(s.String(), "/"), nil
			}
			if remote {
				return strings.TrimRight(src.ResolveReference(s).String(), "/"), nil
			}
		}
	}
	if remote && len(d.Servers) == 0 {
		return src.Scheme + "://" + src.Host, nil
	}
	return "", errors.New("cannot tell where the system runs: the document has no usable servers entry and " +
		"--swagger is not a URL to take it from. Pass --upstream with the system's base URL, " +
		"including its base path, e.g. --upstream https://my-system/api/v3")
}

// exampleGet picks a GET operation with no path parameter for the sample task.
func exampleGet(doc *spec.Document) string {
	first := ""
	for _, op := range doc.Operations {
		if op.Method != http.MethodGet {
			continue
		}
		if first == "" {
			first = op.Path
		}
		if !strings.Contains(op.Path, "{") {
			return op.Path
		}
	}
	if first == "" {
		return "/TODO"
	}
	return first
}

type authSnippet struct {
	login string // the login call, when the type has one
	block string
	env   []string
}

var authOrder = []string{casecfg.AuthCookieSession, casecfg.AuthBearer, casecfg.AuthAPIKey, casecfg.AuthOAuth2}

var authSnippets = map[string]authSnippet{
	casecfg.AuthCookieSession: {
		login: "POST /" + casecfg.Placeholder + "/login",
		block: `  type: cookie-session          # sign in once, then send the session cookie
  login: POST /` + casecfg.Placeholder + `/login       # the login call: method and path
  username_field: username      # JSON field the login body uses for the username
  password_field: password
  login_format: json            # json, or form for application/x-www-form-urlencoded
  username_env: LODOG_UPSTREAM_USER
  password_env: LODOG_UPSTREAM_PASSWORD
  # success:                    # what a real sign-in looks like; default is any 2xx
  #   cookie_name: sid          # for a system that answers a wrong password with 200`,
		env: []string{"LODOG_UPSTREAM_USER", "LODOG_UPSTREAM_PASSWORD"},
	},
	casecfg.AuthBearer: {
		block: `  type: bearer                  # sends Authorization: Bearer <token>
  token_env: LODOG_UPSTREAM_TOKEN   # a fixed token; or sign in for one instead:
  # login: POST /` + casecfg.Placeholder + `/login
  # username_field: username
  # password_field: password
  # login_format: json          # or form
  # username_env: LODOG_UPSTREAM_USER
  # password_env: LODOG_UPSTREAM_PASSWORD
  # token_path: data.token      # where the token is in the login response
  # header: Authorization       # where the token goes, and what comes before it:
  # prefix: "Bearer "           # written as is, trailing space included`,
		env: []string{"LODOG_UPSTREAM_TOKEN"},
	},
	casecfg.AuthAPIKey: {
		block: `  type: api-key                 # a fixed key in a header or in the query string
  key_env: LODOG_UPSTREAM_API_KEY
  header: X-API-Key             # a guess: set exactly one of header or query
  # query: api_key
  # prefix: "ApiKey "           # header only: text put before the key, trailing space included`,
		env: []string{"LODOG_UPSTREAM_API_KEY"},
	},
	casecfg.AuthOAuth2: {
		block: `  type: oauth2-client-credentials   # trades a client id and secret for a token, renews it before it expires
  token_url: /` + casecfg.Placeholder + `/oauth/token  # absolute URL, or a path on the upstream
  client_id_env: LODOG_UPSTREAM_CLIENT_ID
  client_secret_env: LODOG_UPSTREAM_CLIENT_SECRET
  # scope: read write           # omit to ask for the default scope
  # client_auth: basic          # basic (HTTP Basic, default) or body (form fields)
  # header: Authorization       # where the token goes, and what comes before it:
  # prefix: "Bearer "           # written as is, trailing space included`,
		env: []string{"LODOG_UPSTREAM_CLIENT_ID", "LODOG_UPSTREAM_CLIENT_SECRET"},
	},
}

// snippetFor is the generic snippet for typ with the fields the document
// declared filled in.
func snippetFor(typ string, h *authHint) authSnippet {
	sn := authSnippets[typ]
	if h == nil {
		return sn
	}
	switch typ {
	case casecfg.AuthAPIKey:
		place := "  header: " + h.Header + "\n  # query: api_key"
		if h.Query != "" {
			place = "  # header: X-API-Key\n  query: " + h.Query
		}
		sn.block = `  type: api-key                 # a fixed key in a header or in the query string
  key_env: LODOG_UPSTREAM_API_KEY
` + place + `            # set exactly one of header or query
  # prefix: "ApiKey "           # header only: text put before the key, trailing space included`
	case casecfg.AuthOAuth2:
		url := h.TokenURL
		if url == "" {
			url = "/" + casecfg.Placeholder + "/oauth/token"
		}
		scope := "  # scope: read write           # omit to ask for the default scope"
		if h.Scope != "" {
			scope = "  scope: " + h.Scope + "        # the scopes the document lists; keep the ones you need"
		}
		sn.block = strings.Replace(sn.block, "token_url: /"+casecfg.Placeholder+"/oauth/token  # absolute URL, or a path on the upstream",
			"token_url: "+url+"  # absolute URL, or a path on the upstream", 1)
		sn.block = strings.Replace(sn.block, "  # scope: read write           # omit to ask for the default scope", scope, 1)
	}
	return sn
}

// authBlock is the chosen scheme, active, then what was inferred and the
// other schemes as comments, so a user whose system differs sees the other
// shapes without leaving the file.
func authBlock(chosen string, h *authHint, notes []string) string {
	var b strings.Builder
	b.WriteString(snippetFor(chosen, h).block)
	b.WriteString("\n")
	for _, n := range notes {
		b.WriteString("  # " + n + "\n")
	}
	for _, name := range authOrder {
		if name == chosen {
			continue
		}
		b.WriteString("\n  # Instead, for a system that uses " + name + ":\n")
		for _, line := range strings.Split(authSnippets[name].block, "\n") {
			b.WriteString("  # " + strings.TrimPrefix(line, "  ") + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func envHelp(sn authSnippet) string {
	var b strings.Builder
	b.WriteString("   ```bash\n")
	for _, e := range sn.env {
		b.WriteString("   export " + e + "=...\n")
	}
	b.WriteString("   ```")
	return b.String()
}
