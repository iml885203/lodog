package spec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func paramsOf(t *testing.T, doc string, method, path string) map[string]Param {
	t.Helper()
	parsed, err := Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]Param{}
	for _, p := range find(t, parsed, method, path).Params {
		out[p.Name] = p
	}
	return out
}

func TestPetstore3HeaderParameterIsRead(t *testing.T) {
	doc, err := Parse(read(t, "petstore3-openapi3.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Operations) != 19 {
		t.Fatalf("operations = %d, want 19", len(doc.Operations))
	}
	var found bool
	for _, p := range find(t, doc, "DELETE", "/pet/{petId}").Params {
		found = found || (p.Name == "api_key" && p.In == "header" && !p.Required)
	}
	if !found {
		t.Fatal("DELETE /pet/{petId} lost its api_key header parameter")
	}
}

func TestHeaderAndCookieParametersKeepTheirPlace(t *testing.T) {
	got := paramsOf(t, `{"openapi":"3.0.3","paths":{"/x":{"get":{"parameters":[
		{"name":"X-Tenant","in":"header","required":true,"description":"tenant","schema":{"type":"string"}},
		{"name":"theme","in":"cookie","schema":{"type":"string"}}]}}}}`, "GET", "/x")
	if p := got["X-Tenant"]; p.In != "header" || !p.Required || p.Description != "tenant" || p.Schema["type"] != "string" {
		t.Fatalf("X-Tenant = %+v", p)
	}
	if got["theme"].In != "cookie" {
		t.Fatalf("theme = %+v", got["theme"])
	}
}

// OpenAPI 3.0 says a header parameter named Accept, Content-Type or
// Authorization is ignored: the transport owns them.
func TestTransportHeadersAreNotParameters(t *testing.T) {
	got := paramsOf(t, `{"openapi":"3.0.3","paths":{"/x":{"get":{"parameters":[
		{"name":"Authorization","in":"header","schema":{"type":"string"}},
		{"name":"accept","in":"header","schema":{"type":"string"}},
		{"name":"X-Keep","in":"header","schema":{"type":"string"}}]}}}}`, "GET", "/x")
	if len(got) != 1 || got["X-Keep"].In != "header" {
		t.Fatalf("params = %+v, want only X-Keep", got)
	}
}

func TestPathLevelParametersReachEveryOperation(t *testing.T) {
	doc := `{"openapi":"3.0.3","paths":{"/x/{id}":{
		"parameters":[{"name":"id","in":"path","required":true,"schema":{"type":"string"}},
		              {"name":"X-Tenant","in":"header","schema":{"type":"string"}}],
		"get":{},
		"delete":{"parameters":[{"name":"X-Tenant","in":"header","required":true,"schema":{"type":"string"}}]}}}}`
	for _, method := range []string{"GET", "DELETE"} {
		got := paramsOf(t, doc, method, "/x/{id}")
		if got["id"].In != "path" || got["X-Tenant"].In != "header" {
			t.Fatalf("%s params = %+v", method, got)
		}
	}
	if !paramsOf(t, doc, "DELETE", "/x/{id}")["X-Tenant"].Required {
		t.Fatal("the operation's own X-Tenant must replace the path-level one")
	}
}

func TestParameterReferencesAreResolved(t *testing.T) {
	doc := `{"openapi":"3.0.3","paths":{"/x":{"get":{"parameters":[{"$ref":"#/components/parameters/Tenant"}]}}},
		"components":{"parameters":{"Tenant":{"name":"X-Tenant","in":"header","required":true,"schema":{"type":"string"}}}}}`
	if p := paramsOf(t, doc, "GET", "/x")["X-Tenant"]; p.In != "header" || !p.Required {
		t.Fatalf("X-Tenant = %+v", p)
	}
	_, err := Parse([]byte(`{"openapi":"3.0.3","paths":{"/x":{"get":{"parameters":[{"$ref":"#/components/parameters/Nope"}]}}}}`))
	if err == nil || !strings.Contains(err.Error(), "Nope") {
		t.Fatalf("err = %v, want the dangling reference named", err)
	}
}

func TestTheSameNameInTwoPlacesGetsAPlacePrefixOnlyThere(t *testing.T) {
	doc, err := Parse([]byte(`{"openapi":"3.0.3","paths":{"/x/{id}":{"get":{"parameters":[
		{"name":"id","in":"path","required":true,"schema":{"type":"string"}},
		{"name":"id","in":"header","schema":{"type":"string"}},
		{"name":"limit","in":"query","schema":{"type":"integer"}}]}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	args := map[string]string{}
	for _, p := range doc.Operations[0].Params {
		args[p.In+":"+p.Name] = p.ArgName()
	}
	want := map[string]string{"path:id": "path.id", "header:id": "header.id", "query:limit": "limit"}
	for k, v := range want {
		if args[k] != v {
			t.Errorf("%s argument = %q, want %q", k, args[k], v)
		}
	}
}

func TestAnOverlayCanDescribeAHeaderParameter(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	docPath := write("openapi.json", string(read(t, "petstore3-openapi3.json")))
	ovPath := write("overlay.yaml", `overlay: 1.0.0
info: {title: t, version: "1"}
actions:
  - target: "$.paths['/pet/{petId}'].delete.parameters[?(@.name=='api_key')]"
    update: {description: "partner key, ask the operator"}
`)
	doc, err := LoadFileWithOverlay(docPath, ovPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range find(t, doc, "DELETE", "/pet/{petId}").Params {
		if p.Name == "api_key" && p.Description == "partner key, ask the operator" {
			return
		}
	}
	t.Fatal("the overlay did not reach the api_key header parameter")
}

func TestEveryJSONFlavourOfABodyIsRead(t *testing.T) {
	for _, ct := range []string{
		"application/json", "application/json; charset=utf-8", "application/vnd.api+json",
		"application/problem+json", "application/merge-patch+json; profile=x",
	} {
		doc, err := Parse([]byte(`{"openapi":"3.0.3","paths":{"/x":{"post":{"requestBody":{"required":true,"content":{
			"` + ct + `":{"schema":{"type":"object","properties":{"a":{"type":"string"}}}}}}}}}}`))
		if err != nil {
			t.Fatal(err)
		}
		op := doc.Operations[0]
		if op.Body == nil || !op.BodyRequired || op.BodyContentType != ct || !IsJSON(ct) {
			t.Errorf("%s: body %v required %v type %q", ct, op.Body, op.BodyRequired, op.BodyContentType)
		}
	}
}

func TestPlainJSONIsPreferredOverOtherFlavoursAndOverMultipart(t *testing.T) {
	doc, err := Parse([]byte(`{"openapi":"3.0.3","paths":{"/x":{"post":{"requestBody":{"content":{
		"multipart/form-data":{"schema":{"type":"object"}},
		"application/vnd.api+json":{"schema":{"type":"object","title":"flavour"}},
		"application/json":{"schema":{"type":"object","title":"plain"}}}}}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if op := doc.Operations[0]; op.BodyContentType != "application/json" || op.Body["title"] != "plain" {
		t.Fatalf("type %q body %v", op.BodyContentType, op.Body)
	}
}

func TestIsJSONRefusesNonJSONTypes(t *testing.T) {
	for _, ct := range []string{"application/xml", "text/plain", "multipart/form-data", "application/x-www-form-urlencoded", "application/jsonp", "", "garbage;;"} {
		if IsJSON(ct) {
			t.Errorf("%q counted as JSON", ct)
		}
	}
}

func TestAFormBodyIsReadUrlencodedBeforeMultipartAndAfterJSON(t *testing.T) {
	doc, err := Parse([]byte(`{"openapi":"3.0.3","paths":{
		"/forms":{"post":{"requestBody":{"required":true,"content":{
			"multipart/form-data":{"schema":{"type":"object","title":"multipart"}},
			"application/x-www-form-urlencoded":{"schema":{"type":"object","title":"form"}}}}}},
		"/both":{"post":{"requestBody":{"content":{
			"application/x-www-form-urlencoded":{"schema":{"type":"object","title":"form"}},
			"application/json":{"schema":{"type":"object","title":"json"}}}}}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	both, forms := doc.Operations[0], doc.Operations[1]
	if forms.BodyContentType != "application/x-www-form-urlencoded" || forms.Body["title"] != "form" || !forms.BodyRequired {
		t.Fatalf("forms: type %q body %v required %v", forms.BodyContentType, forms.Body, forms.BodyRequired)
	}
	if both.BodyContentType != "application/json" || both.Body["title"] != "json" {
		t.Fatalf("both: type %q body %v", both.BodyContentType, both.Body)
	}
}
