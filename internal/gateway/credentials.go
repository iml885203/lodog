package gateway

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/iml885203/lodog/internal/spec"
)

// buildHeaders maps header and cookie arguments onto request headers. buildCall has
// already refused a missing required argument.
func buildHeaders(op spec.Operation, args map[string]any) (http.Header, error) {
	header := http.Header{}
	var cookies []string
	for _, p := range op.Params {
		v, ok := args[p.ArgName()]
		if !ok || v == nil || (p.In != "header" && p.In != "cookie") {
			continue
		}
		value := scalarList(v)
		if strings.ContainsAny(value, "\r\n") {
			return nil, fmt.Errorf("argument %q contains a line break", p.ArgName())
		}
		if p.In == "header" {
			header.Set(p.Name, value)
		} else {
			cookies = append(cookies, p.Name+"="+url.QueryEscape(value))
		}
	}
	// One Cookie header; a cookie jar appends its own cookies to it.
	if len(cookies) > 0 {
		header.Set("Cookie", strings.Join(cookies, "; "))
	}
	return header, nil
}

// scalarList renders an array argument as comma-separated, the OpenAPI
// "simple" style a header or cookie uses.
func scalarList(v any) string {
	list, ok := v.([]any)
	if !ok {
		return scalar(v)
	}
	parts := make([]string, len(list))
	for i, item := range list {
		parts[i] = scalar(item)
	}
	return strings.Join(parts, ",")
}
