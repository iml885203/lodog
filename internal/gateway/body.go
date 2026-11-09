package gateway

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"mime"
	"mime/multipart"
	"net/textproto"
	"net/url"
	"sort"
	"strings"

	"github.com/iml885203/lodog/internal/serving"

	"github.com/iml885203/lodog/internal/spec"
)

// encodeBody renders the requestBody argument in the operation's declared
// type and returns the Content-Type to send it with.
func encodeBody(op spec.Operation, v any) ([]byte, string, error) {
	if !serving.IsForm(op.BodyContentType) {
		encoded, err := json.Marshal(v)
		if err != nil {
			return nil, "", fmt.Errorf("argument %q: %w", serving.BodyArg, err)
		}
		return encoded, op.BodyContentType, nil
	}
	fields, ok := v.(map[string]any)
	if !ok {
		return nil, "", fmt.Errorf("argument %q must be an object of form fields", serving.BodyArg)
	}
	if serving.MediaType(op.BodyContentType) == serving.FormMultipart {
		return encodeMultipart(fields, serving.FileFields(op))
	}
	form := url.Values{}
	for _, name := range sortedKeys(fields) {
		values, _, err := formValues(name, fields[name])
		if err != nil {
			return nil, "", err
		}
		form[name] = values
	}
	return []byte(form.Encode()), serving.FormURLEncoded, nil
}

// formValues renders one form field the way OpenAPI encodes a form by
// default: a scalar as itself, a list of scalars as the field repeated, and
// anything holding an object as JSON text (asJSON).
func formValues(name string, v any) (values []string, asJSON bool, err error) {
	switch x := v.(type) {
	case nil:
		return nil, false, nil
	case map[string]any:
		return jsonValue(name, x)
	case []any:
		for _, item := range x {
			switch item.(type) {
			case map[string]any, []any:
				return jsonValue(name, x)
			}
		}
		out := make([]string, len(x))
		for i, item := range x {
			out[i] = scalar(item)
		}
		return out, false, nil
	default:
		return []string{scalar(v)}, false, nil
	}
}

func jsonValue(name string, v any) ([]string, bool, error) {
	encoded, err := json.Marshal(v)
	if err != nil {
		return nil, false, fmt.Errorf("form field %q: %w", name, err)
	}
	return []string{string(encoded)}, true, nil
}

func encodeMultipart(fields map[string]any, files map[string]bool) ([]byte, string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	sent := 0
	for _, name := range sortedKeys(fields) {
		var err error
		if files[name] {
			sent, err = writeFiles(w, name, fields[name], sent)
		} else {
			err = writeFields(w, name, fields[name])
		}
		if err != nil {
			return nil, "", err
		}
	}
	if err := w.Close(); err != nil {
		return nil, "", fmt.Errorf("encode multipart body: %w", err)
	}
	return buf.Bytes(), w.FormDataContentType(), nil
}

func writeFields(w *multipart.Writer, name string, v any) error {
	values, asJSON, err := formValues(name, v)
	if err != nil {
		return err
	}
	for _, value := range values {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"`, quoteEscaper.Replace(name)))
		if asJSON {
			h.Set("Content-Type", "application/json")
		}
		part, err := w.CreatePart(h)
		if err != nil {
			return fmt.Errorf("form field %q: %w", name, err)
		}
		if _, err := part.Write([]byte(value)); err != nil {
			return fmt.Errorf("form field %q: %w", name, err)
		}
	}
	return nil
}

func writeFiles(w *multipart.Writer, name string, v any, sent int) (int, error) {
	items := []any{v}
	if list, ok := v.([]any); ok {
		items = list
	}
	for _, item := range items {
		f, err := readFile(name, item)
		if err != nil {
			return sent, err
		}
		if sent += len(f.data); sent > serving.MaxUploadBytes {
			return sent, fmt.Errorf("argument %q: the files of one call may hold at most %d MiB", serving.BodyArg, serving.MaxUploadBytes>>20)
		}
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`,
			quoteEscaper.Replace(name), quoteEscaper.Replace(f.name)))
		h.Set("Content-Type", f.contentType)
		part, err := w.CreatePart(h)
		if err != nil {
			return sent, fmt.Errorf("file field %q: %w", name, err)
		}
		if _, err := part.Write(f.data); err != nil {
			return sent, fmt.Errorf("file field %q: %w", name, err)
		}
	}
	return sent, nil
}

var quoteEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`)

type file struct {
	name, contentType string
	data              []byte
}

// readFile reads one file argument. A value that cannot make a well-formed
// part is an argument error, refused before any approval is requested.
func readFile(field string, v any) (file, error) {
	obj, ok := v.(map[string]any)
	content, _ := obj[serving.FileContent].(string)
	if !ok || content == "" {
		return file{}, fmt.Errorf("file field %q must be an object with %s", field, serving.FileContent)
	}
	if base64.StdEncoding.DecodedLen(len(content)) > serving.MaxUploadBytes+3 {
		return file{}, fmt.Errorf("argument %q: the files of one call may hold at most %d MiB", serving.BodyArg, serving.MaxUploadBytes>>20)
	}
	data, err := base64.StdEncoding.DecodeString(content)
	if err != nil {
		return file{}, fmt.Errorf("file field %q: %s is not standard base64: %w", field, serving.FileContent, err)
	}
	f := file{name: field, contentType: "application/octet-stream", data: data}
	if name, ok := obj[serving.FileName].(string); ok && name != "" {
		f.name = name
	}
	if ct, ok := obj[serving.FileType].(string); ok && ct != "" {
		if _, _, err := mime.ParseMediaType(ct); err != nil {
			return file{}, fmt.Errorf("file field %q: %s %q is not a media type", field, serving.FileType, ct)
		}
		f.contentType = ct
	}
	if strings.ContainsAny(f.name+f.contentType, "\r\n") {
		return file{}, fmt.Errorf("file field %q contains a line break", field)
	}
	return f, nil
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
