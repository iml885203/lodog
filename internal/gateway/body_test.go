package gateway

import (
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/iml885203/lodog/internal/serving"

	"github.com/iml885203/lodog/internal/casecfg"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/iml885203/lodog/internal/spec"
)

// The public Swagger 2.0 Petstore has both form operations: updatePetWithForm
// (urlencoded) and uploadFile (multipart with a file).
const formDoc = "../spec/testdata/petstore-swagger2.json"

// received is one request as the legacy system read it, body included.
type received struct {
	path, contentType string
	body              []byte
}

type bodyRecorder struct {
	mu   sync.Mutex
	seen []received
}

func (b *bodyRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	b.mu.Lock()
	b.seen = append(b.seen, received{path: r.URL.Path, contentType: r.Header.Get("Content-Type"), body: body})
	b.mu.Unlock()
	_, _ = io.WriteString(w, `{}`)
}

func (b *bodyRecorder) all() []received {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]received(nil), b.seen...)
}

func formGateway(t *testing.T) (*mcp.ClientSession, *Gateway, *bodyRecorder) {
	t.Helper()
	legacy := &bodyRecorder{}
	upstream := httptest.NewServer(legacy)
	t.Cleanup(upstream.Close)
	source, err := filepath.Abs(formDoc)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "spec"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(source, filepath.Join(dir, "spec", "openapi.json")); err != nil {
		t.Fatal(err)
	}
	cfg := &casecfg.Config{
		Case: dir, Level: casecfg.L0, Listen: "127.0.0.1:0", Upstream: upstream.URL,
		Auth: casecfg.AuthConfig{Type: casecfg.AuthAPIKey, KeyEnv: "K", Header: "api_key"}, Identity: casecfg.IdentityConfig{Actor: "agent:test"},
	}
	gw, err := New(cfg, func(string) string { return "k" }, Labels{}, "test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(gw.Handler())
	t.Cleanup(srv.Close)
	session, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil).
		Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: srv.URL + MCPPath}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session, gw, legacy
}

func callTool(t *testing.T, session *mcp.ClientSession, name string, args map[string]any) (bool, string) {
	t.Helper()
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	return res.IsError, res.Content[0].(*mcp.TextContent).Text
}

func TestBothPetstoreFormOperationsAreServed(t *testing.T) {
	_, gw, _ := formGateway(t)
	tools, skipped := gw.Tools()
	if len(skipped) != 0 {
		t.Fatalf("skipped %v: every Swagger 2.0 Petstore operation has a body the gateway forwards", skipped)
	}
	for _, tl := range tools {
		if tl.Tool.Name != "uploadFile" {
			continue
		}
		body := tl.Tool.InputSchema["properties"].(map[string]any)[serving.BodyArg].(map[string]any)
		file := body["properties"].(map[string]any)["file"].(map[string]any)
		if file["required"].([]any)[0] != serving.FileContent {
			t.Fatalf("file field schema = %v: the agent must be asked for %s", file, serving.FileContent)
		}
		return
	}
	t.Fatal("uploadFile is not served")
}

func TestAURLEncodedFormReachesTheLegacySystemAsAForm(t *testing.T) {
	session, _, legacy := formGateway(t)
	isErr, text := callTool(t, session, "updatePetWithForm", map[string]any{
		"petId": 7, "requestBody": map[string]any{"name": "rex", "status": "sold"},
	})
	if isErr {
		t.Fatalf("call refused: %s", text)
	}
	got := legacy.all()
	if len(got) != 1 || got[0].path != "/pet/7" || got[0].contentType != serving.FormURLEncoded {
		t.Fatalf("legacy got %+v", got)
	}
	form, err := url.ParseQuery(string(got[0].body))
	if err != nil || form.Get("name") != "rex" || form.Get("status") != "sold" || len(form) != 2 {
		t.Fatalf("form = %v (%v), body %q", form, err, got[0].body)
	}
}

func TestAMultipartUploadCarriesItsFieldsAndTheFile(t *testing.T) {
	session, _, legacy := formGateway(t)
	content := []byte("\x89PNG\r\n\x1a\nfake image")
	isErr, text := callTool(t, session, "uploadFile", map[string]any{
		"petId": 7,
		"requestBody": map[string]any{
			"additionalMetadata": "front",
			"file":               map[string]any{serving.FileContent: base64.StdEncoding.EncodeToString(content), serving.FileName: "rex.png", serving.FileType: "image/png"},
		},
	})
	if isErr {
		t.Fatalf("call refused: %s", text)
	}
	got := legacy.all()
	if len(got) != 1 || got[0].path != "/pet/7/uploadImage" {
		t.Fatalf("legacy got %+v", got)
	}
	parts := readMultipart(t, got[0])
	if parts["additionalMetadata"].data != "front" {
		t.Errorf("additionalMetadata = %+v", parts["additionalMetadata"])
	}
	if f := parts["file"]; f.data != string(content) || f.filename != "rex.png" || f.contentType != "image/png" {
		t.Errorf("file part = %+v", f)
	}
}

type part struct{ data, filename, contentType string }

func readMultipart(t *testing.T, r received) map[string]part {
	t.Helper()
	base, params, err := mime.ParseMediaType(r.contentType)
	if err != nil || base != serving.FormMultipart {
		t.Fatalf("Content-Type = %q", r.contentType)
	}
	reader := multipart.NewReader(strings.NewReader(string(r.body)), params["boundary"])
	out := map[string]part{}
	for {
		p, err := reader.NextPart()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(p)
		out[p.FormName()] = part{data: string(data), filename: p.FileName(), contentType: p.Header.Get("Content-Type")}
	}
}

func TestAFileThatIsNotBase64IsRefusedBeforeForwarding(t *testing.T) {
	session, _, legacy := formGateway(t)
	isErr, text := callTool(t, session, "uploadFile", map[string]any{
		"petId": 7, "requestBody": map[string]any{"file": map[string]any{serving.FileContent: "not base64!"}},
	})
	if !isErr || !strings.Contains(text, "not standard base64") || len(legacy.all()) != 0 {
		t.Fatalf("got %v %s, legacy saw %d calls", isErr, text, len(legacy.all()))
	}
	isErr, text = callTool(t, session, "uploadFile", map[string]any{
		"petId": 7, "requestBody": map[string]any{"file": "/etc/passwd"},
	})
	if !isErr || !strings.Contains(text, serving.FileContent) || len(legacy.all()) != 0 {
		t.Fatalf("a path instead of file content: %v %s", isErr, text)
	}
}

var formOp = spec.Operation{
	Method: "POST", Path: "/f", BodyContentType: serving.FormMultipart,
	Body: map[string]any{"type": "object", "properties": map[string]any{
		"tags":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"meta":  map[string]any{"type": "object"},
		"count": map[string]any{"type": "integer"},
		"files": map[string]any{"type": "array", "items": map[string]any{"type": "string", "format": "binary"}},
	}},
}

func TestFormFieldsFollowOpenAPIsDefaultEncoding(t *testing.T) {
	op := formOp
	op.BodyContentType = serving.FormURLEncoded
	body, ct, err := encodeBody(op, map[string]any{"tags": []any{"a", "b"}, "meta": map[string]any{"k": 1.0}, "count": 3.0})
	if err != nil || ct != serving.FormURLEncoded {
		t.Fatalf("ct %q err %v", ct, err)
	}
	form, _ := url.ParseQuery(string(body))
	if strings.Join(form["tags"], ",") != "a,b" || form.Get("meta") != `{"k":1}` || form.Get("count") != "3" {
		t.Fatalf("form = %v", form)
	}
	if _, _, err := encodeBody(op, []any{"x"}); err == nil {
		t.Fatal("a form body that is not an object must be refused")
	}
}

func TestTheFilesOfOneCallAreBounded(t *testing.T) {
	half := base64.StdEncoding.EncodeToString(make([]byte, serving.MaxUploadBytes/2+1))
	file := map[string]any{serving.FileContent: half}
	if _, _, err := encodeBody(formOp, map[string]any{"files": []any{file}}); err != nil {
		t.Fatalf("one file under the limit: %v", err)
	}
	if _, _, err := encodeBody(formOp, map[string]any{"files": []any{file, file}}); err == nil || !strings.Contains(err.Error(), "MiB") {
		t.Fatalf("two files over the limit together: err = %v", err)
	}
}
