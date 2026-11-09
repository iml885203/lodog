package serving

import (
	"fmt"
	"maps"
	"mime"

	"github.com/iml885203/lodog/internal/spec"
)

// The request body forms the gateway forwards. Any other declared type
// (XML, plain text, a raw binary body) is reported as not served.
const (
	FormURLEncoded = "application/x-www-form-urlencoded"
	FormMultipart  = "multipart/form-data"
)

// MaxUploadBytes bounds the decoded file content one call may send. It is a
// safety limit, like the gateway's maxUpstreamBody, not a statement about what the legacy
// system accepts.
const MaxUploadBytes = 8 << 20

// File argument fields: a file travels inside the call as base64, so the
// approval hash covers its content and the agent never names a path on the
// gateway's machine.
const (
	FileContent = "contentBase64"
	FileName    = "filename"
	FileType    = "contentType"
)

// MediaType is contentType without its parameters, or "" when it is not a media type.
func MediaType(contentType string) string {
	base, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return ""
	}
	return base
}

// IsForm reports whether contentType is a form body the gateway forwards.
func IsForm(contentType string) bool {
	base := MediaType(contentType)
	return base == FormURLEncoded || base == FormMultipart
}

// unservable says why the gateway cannot forward op's body, or "" when it can.
func unservable(op spec.Operation) string {
	switch {
	case op.BodyContentType == "" || spec.IsJSON(op.BodyContentType):
		return ""
	case !IsForm(op.BodyContentType):
		return op.BodyContentType + " bodies are not forwarded"
	case !isObject(op.Body):
		return op.BodyContentType + " body schema is not an object, so it has no fields to send"
	}
	return ""
}

func isObject(schema map[string]any) bool {
	if schema == nil {
		return false
	}
	_, hasProps := schema["properties"]
	return schema["type"] == "object" || hasProps
}

// isFile is a schema the document marks as file content.
func isFile(schema map[string]any) bool {
	if schema["type"] != "string" {
		return false
	}
	_, mediaTyped := schema["contentMediaType"]
	_, encoded := schema["contentEncoding"]
	return schema["format"] == "binary" || schema["format"] == "base64" || mediaTyped || encoded
}

// FileFields names the multipart fields that carry a file, or a list of files.
func FileFields(op spec.Operation) map[string]bool {
	out := map[string]bool{}
	if MediaType(op.BodyContentType) != FormMultipart {
		return out
	}
	props, _ := op.Body["properties"].(map[string]any)
	for name, raw := range props {
		s, _ := raw.(map[string]any)
		items, _ := s["items"].(map[string]any)
		if isFile(s) || (s["type"] == "array" && isFile(items)) {
			out[name] = true
		}
	}
	return out
}

func fileArgSchema(description any) map[string]any {
	text := "A file, sent inside the call: its bytes as standard base64 in " + FileContent +
		fmt.Sprintf(" (all files of one call together at most %d MiB).", MaxUploadBytes>>20)
	if d, ok := description.(string); ok && d != "" {
		text = d + "\n" + text
	}
	return map[string]any{
		"type":        "object",
		"description": text,
		"properties": map[string]any{
			FileContent: map[string]any{"type": "string", "contentEncoding": "base64"},
			FileName:    map[string]any{"type": "string", "description": "Defaults to the field name."},
			FileType:    map[string]any{"type": "string", "description": "Defaults to application/octet-stream."},
		},
		"required": []any{FileContent},
	}
}

// bodySchema is the requestBody argument as the agent sees it: the document's
// schema, with each multipart file field asking for a file object.
func bodySchema(op spec.Operation) map[string]any {
	files := FileFields(op)
	if len(files) == 0 {
		return op.Body
	}
	props := maps.Clone(op.Body["properties"].(map[string]any))
	for name := range files {
		s, _ := props[name].(map[string]any)
		if s["type"] == "array" {
			list := map[string]any{"type": "array", "items": fileArgSchema(nil)}
			if d, ok := s["description"]; ok {
				list["description"] = d
			}
			props[name] = list
			continue
		}
		props[name] = fileArgSchema(s["description"])
	}
	out := maps.Clone(op.Body)
	out["properties"] = props
	return out
}
