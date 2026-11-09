package spec

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

func read(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name) //nolint:gosec // G304: fixed test file names
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// normalized returns the 3.x JSON Normalize produced, decoded for inspection.
func normalized(t *testing.T, data []byte) map[string]any {
	t.Helper()
	out, err := Normalize(data)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("Normalize returned invalid JSON: %v", err)
	}
	return doc
}

func dig(t *testing.T, node any, path ...string) any {
	t.Helper()
	for _, key := range path {
		m, ok := node.(map[string]any)
		if !ok {
			t.Fatalf("at %q: not an object: %v", key, node)
		}
		if node, ok = m[key]; !ok {
			t.Fatalf("missing %q (path %v)", key, path)
		}
	}
	return node
}

func TestAnOpenAPI3JSONDocumentIsReturnedUntouched(t *testing.T) {
	data, err := os.ReadFile(backofficeDoc)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Normalize(data)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != string(data) {
		t.Fatal("a 3.0 JSON document must pass through byte for byte")
	}
}

func TestPetstoreSwagger2ConvertsEveryOperation(t *testing.T) {
	doc := normalized(t, read(t, "petstore-swagger2.json"))
	if dig(t, doc, "openapi") != "3.0.3" {
		t.Fatalf("openapi = %v", doc["openapi"])
	}
	if _, ok := doc["swagger"]; ok {
		t.Fatal("swagger field must not survive")
	}
	count := 0
	for _, item := range dig(t, doc, "paths").(map[string]any) {
		count += len(item.(map[string]any))
	}
	if count != 20 {
		t.Fatalf("operations = %d, want 20 (petstore 2.0 declares 20)", count)
	}
	if got := dig(t, doc, "servers").([]any)[0].(map[string]any)["url"]; got != "https://petstore.swagger.io/v2" {
		t.Fatalf("server = %v", got)
	}
}

func TestSwagger2BodyBecomesRequestBodyForEachConsumedType(t *testing.T) {
	doc := normalized(t, read(t, "petstore-swagger2.json"))
	post := dig(t, doc, "paths", "/pet", "post").(map[string]any)
	if _, ok := post["parameters"]; ok {
		t.Fatalf("body parameter must leave parameters: %v", post["parameters"])
	}
	content := dig(t, post, "requestBody", "content").(map[string]any)
	for _, ct := range []string{"application/json", "application/xml"} {
		ref := dig(t, content, ct, "schema", "$ref")
		if ref != "#/components/schemas/Pet" {
			t.Fatalf("%s schema ref = %v", ct, ref)
		}
	}
	if dig(t, post, "requestBody", "required") != true {
		t.Fatal("body required flag lost")
	}
	if _, ok := dig(t, doc, "components", "schemas").(map[string]any)["Pet"]; !ok {
		t.Fatal("definitions did not become components/schemas")
	}
	if strings.Contains(string(mustNormalize(t, read(t, "petstore-swagger2.json"))), "#/definitions/") {
		t.Fatal("a #/definitions reference survived")
	}
}

func mustNormalize(t *testing.T, data []byte) []byte {
	t.Helper()
	out, err := Normalize(data)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSwagger2FormDataBecomesAMultipartObjectWithABinaryFile(t *testing.T) {
	doc := normalized(t, read(t, "petstore-swagger2.json"))
	schema := dig(t, doc, "paths", "/pet/{petId}/uploadImage", "post", "requestBody", "content", "multipart/form-data", "schema")
	file := dig(t, schema, "properties", "file").(map[string]any)
	if file["type"] != "string" || file["format"] != "binary" {
		t.Fatalf("file = %v, want a binary string", file)
	}
	urlencoded := dig(t, doc, "paths", "/pet/{petId}", "post", "requestBody", "content")
	if _, ok := urlencoded.(map[string]any)["application/x-www-form-urlencoded"]; !ok {
		t.Fatalf("form content = %v", urlencoded)
	}
}

func TestPetstoreSwagger2ReadsWholeIncludingItsHeaderParameter(t *testing.T) {
	doc, err := Parse(read(t, "petstore-swagger2.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Operations) != 20 {
		t.Fatalf("operations = %d, want 20", len(doc.Operations))
	}
	del := find(t, doc, "DELETE", "/pet/{petId}")
	got := map[string]Param{}
	for _, p := range del.Params {
		got[p.Name] = p
	}
	if got["api_key"].In != "header" || got["api_key"].Required || got["petId"].In != "path" {
		t.Fatalf("params = %+v, want optional header api_key and path petId", del.Params)
	}
}

func featuresDoc(t *testing.T) *Document {
	t.Helper()
	doc, err := Parse(read(t, "swagger2-features.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Operations) != 4 {
		t.Fatalf("operations = %d, want 4", len(doc.Operations))
	}
	return doc
}

func TestSwagger2ParametersMergePathLevelAndReferencedOnes(t *testing.T) {
	doc := featuresDoc(t)
	list := find(t, doc, "GET", "/pets")
	params := map[string]Param{}
	for _, p := range list.Params {
		params[p.Name] = p
	}
	if len(params) != 3 {
		t.Fatalf("params = %v, want limit, tenant, tags", list.Params)
	}
	if params["limit"].Schema["maximum"] == nil || params["limit"].Schema["type"] != "integer" {
		t.Fatalf("limit (from #/parameters) = %v", params["limit"])
	}
	if !params["tenant"].Required {
		t.Fatal("the operation's tenant must replace the path-level one")
	}
	if params["tags"].Schema["type"] != "array" {
		t.Fatalf("tags = %v", params["tags"])
	}
	if !list.Secured {
		t.Fatal("document-level security lost")
	}
}

func TestSwagger2BodiesAndResponsesReadThroughParse(t *testing.T) {
	doc := featuresDoc(t)
	create := find(t, doc, "POST", "/pets")
	if create.BodyContentType != "application/json" || !create.BodyRequired {
		t.Fatalf("body = %q required=%v", create.BodyContentType, create.BodyRequired)
	}
	props := create.Body["properties"].(map[string]any)
	if props["nick"].(map[string]any)["nullable"] != true {
		t.Fatalf("x-nullable not carried: %v", props["nick"])
	}
	owner := props["owner"].(map[string]any)["properties"].(map[string]any)["pets"].(map[string]any)
	if owner["items"].(map[string]any)["type"] != "object" {
		t.Fatalf("nested definitions not inlined: %v", owner)
	}
	if !reflect.DeepEqual(create.Responses, []string{"201", "default"}) {
		t.Fatalf("responses = %v", create.Responses)
	}
}

func TestSwagger2FormAndSecurityReadThroughParse(t *testing.T) {
	doc := featuresDoc(t)
	upload := find(t, doc, "POST", "/pets/{id}/photo")
	if upload.BodyContentType != "multipart/form-data" {
		t.Fatalf("upload content type = %q", upload.BodyContentType)
	}
	if req := upload.Body["required"].([]any); len(req) != 1 || req[0] != "caption" {
		t.Fatalf("upload required = %v", req)
	}
	if find(t, doc, "POST", "/login").Secured {
		t.Fatal("security: [] must opt the operation out")
	}
	if got := strings.Join(doc.SecuritySchemes, ","); got != "basic,key,oauth" {
		t.Fatalf("security schemes = %s", got)
	}
}

func TestSwagger2SecurityDefinitionsMapToOpenAPI3Schemes(t *testing.T) {
	doc := normalized(t, read(t, "swagger2-features.yaml"))
	schemes := dig(t, doc, "components", "securitySchemes")
	if b := dig(t, schemes, "basic").(map[string]any); b["type"] != "http" || b["scheme"] != "basic" {
		t.Fatalf("basic = %v", b)
	}
	flow := dig(t, schemes, "oauth", "flows", "authorizationCode").(map[string]any)
	if flow["tokenUrl"] != "https://auth.example.com/token" || flow["authorizationUrl"] == nil {
		t.Fatalf("oauth flow = %v", flow)
	}
}

func TestOpenAPI3YAMLIsRead(t *testing.T) {
	for name, wantOps := range map[string]int{"petstore-openapi3.yaml": 3, "petstore-expanded-openapi3.yaml": 4} {
		doc, err := Parse(read(t, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(doc.Operations) != wantOps {
			t.Fatalf("%s: operations = %d, want %d", name, len(doc.Operations), wantOps)
		}
	}
	doc, err := Parse(read(t, "petstore-openapi3.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	list := find(t, doc, "GET", "/pets")
	if len(list.Params) != 1 || list.Params[0].Name != "limit" || list.Params[0].In != "query" {
		t.Fatalf("GET /pets params = %v", list.Params)
	}
	create := find(t, doc, "POST", "/pets")
	if create.Summary != "Create a pet" {
		t.Fatalf("summary = %q", create.Summary)
	}
}

func TestYAMLAndJSONOfTheSameDocumentReadTheSame(t *testing.T) {
	yamlDoc := "openapi: 3.0.3\npaths:\n  /a/{id}:\n    get:\n      parameters:\n        - {name: id, in: path, required: true, schema: {type: integer}}\n      responses:\n        200: {description: ok}\n"
	jsonDoc := `{"openapi":"3.0.3","paths":{"/a/{id}":{"get":{"parameters":[{"name":"id","in":"path","required":true,"schema":{"type":"integer"}}],"responses":{"200":{"description":"ok"}}}}}}`
	fromYAML, err := Parse([]byte(yamlDoc))
	if err != nil {
		t.Fatal(err)
	}
	fromJSON, err := Parse([]byte(jsonDoc))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fromYAML.Operations, fromJSON.Operations) {
		t.Fatalf("yaml %+v != json %+v", fromYAML.Operations, fromJSON.Operations)
	}
	if !reflect.DeepEqual(fromYAML.Operations[0].Responses, []string{"200"}) {
		t.Fatalf("an unquoted YAML 200 must read as response code 200: %v", fromYAML.Operations[0].Responses)
	}
}

func TestOpenAPI31IsReadWithoutLosingItsNewKeywords(t *testing.T) {
	doc, err := Parse([]byte(`{"openapi": "3.1.0", "paths": {"/x": {"post": {"requestBody": {"content": {
		"application/json": {"schema": {"type": "object", "properties": {
			"name": {"type": ["string", "null"]},
			"age": {"type": "integer", "exclusiveMinimum": 0}}}}}}}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	props := doc.Operations[0].Body["properties"].(map[string]any)
	if !reflect.DeepEqual(props["name"].(map[string]any)["type"], []any{"string", "null"}) {
		t.Fatalf("type array lost: %v", props["name"])
	}
	if props["age"].(map[string]any)["exclusiveMinimum"] != json.Number("0") && props["age"].(map[string]any)["exclusiveMinimum"] != float64(0) {
		t.Fatalf("numeric exclusiveMinimum lost: %v", props["age"])
	}
}

func TestUnsupportedVersionsAreRefusedWithTheSupportedList(t *testing.T) {
	for name, doc := range map[string]string{
		"swagger 1.2":     `{"swagger": "1.2", "paths": {}}`,
		"openapi 4":       `{"openapi": "4.0.0", "paths": {}}`,
		"openapi 3.2":     `{"openapi": "3.2.0", "paths": {}}`,
		"no version":      `{"paths": {}}`,
		"yaml no version": "paths: {}\n",
	} {
		_, err := Parse([]byte(doc))
		if err == nil || !strings.Contains(err.Error(), "Swagger 2.0, OpenAPI 3.0.x and OpenAPI 3.1.x") {
			t.Fatalf("%s: err = %v, want the supported versions named", name, err)
		}
	}
}

func TestBrokenInputIsAnErrorNotAnEmptyDocument(t *testing.T) {
	for name, doc := range map[string]string{
		"empty":        "",
		"broken json":  `{"openapi": "3.0.0",`,
		"broken yaml":  "openapi: [3.0.0\n",
		"top-level []": "- a\n- b\n",
	} {
		if _, err := Parse([]byte(doc)); err == nil {
			t.Fatalf("%s: want an error", name)
		}
	}
}

func TestSwagger2ConstructsWithNoEquivalentAreErrorsThatNameTheLocation(t *testing.T) {
	cases := map[string]struct{ doc, want string }{
		"file in a response schema": {
			`{"swagger":"2.0","paths":{"/f":{"get":{"responses":{"200":{"description":"ok","schema":{"type":"object","properties":{"f":{"type":"file"}}}}}}}}}`,
			"paths./f.get.responses.200.schema.properties.f",
		},
		"file in a query parameter": {
			`{"swagger":"2.0","paths":{"/f":{"get":{"parameters":[{"name":"f","in":"query","type":"file"}],"responses":{}}}}}`,
			"paths./f.get.parameters[0] (f)",
		},
		"external reference": {
			`{"swagger":"2.0","paths":{"/f":{"post":{"parameters":[{"name":"b","in":"body","schema":{"$ref":"other.json#/Pet"}}],"responses":{}}}}}`,
			"paths./f.post.parameters (body).schema.$ref",
		},
		"body and formData together": {
			`{"swagger":"2.0","paths":{"/f":{"post":{"parameters":[{"name":"b","in":"body","schema":{}},{"name":"c","in":"formData","type":"string"}],"responses":{}}}}}`,
			"paths./f.post.parameters[1] (c)",
		},
		"two body parameters": {
			`{"swagger":"2.0","paths":{"/f":{"post":{"parameters":[{"name":"a","in":"body","schema":{}},{"name":"b","in":"body","schema":{}}],"responses":{}}}}}`,
			"paths./f.post.parameters[1] (b)",
		},
		"unknown parameter reference": {
			`{"swagger":"2.0","paths":{"/f":{"get":{"parameters":[{"$ref":"#/parameters/nope"}],"responses":{}}}}}`,
			"paths./f.get.parameters[0].$ref",
		},
		"tsv collection format": {
			`{"swagger":"2.0","paths":{"/f":{"get":{"parameters":[{"name":"a","in":"query","type":"array","items":{"type":"string"},"collectionFormat":"tsv"}],"responses":{}}}}}`,
			"collectionFormat",
		},
		"unknown security type": {
			`{"swagger":"2.0","securityDefinitions":{"x":{"type":"mutual-tls"}},"paths":{}}`,
			"securityDefinitions.x.type",
		},
		"unknown top-level field": {
			`{"swagger":"2.0","paths":{},"webhooks":{}}`,
			"webhooks",
		},
	}
	for name, c := range cases {
		_, err := Normalize([]byte(c.doc))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s: err = %v, want it to mention %q", name, err, c.want)
		}
	}
}
