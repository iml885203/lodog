package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
)

// mcpProtocolCeiling is the newest MCP protocol version the gateway
// negotiates. go-sdk offers 2026-07-28, which requires an Mcp-Method header on
// every request; Claude Code asks for 2026-07-28 but sends no such header, so
// the handshake would succeed and the first tool call be refused. Same as
// the MCP transport implementation. Raise it when the clients in use send
// what the newer versions require.
const mcpProtocolCeiling = "2025-06-18"

// metaProtocolVersion is where a 2026-07-28 client may state its version
// instead of params.protocolVersion (SEP-2575).
const metaProtocolVersion = "io.modelcontextprotocol/protocolVersion"

// capProtocol holds every request at mcpProtocolCeiling: the header, and the
// body's own statements of version where they appear. Versions are dates, so
// string comparison orders them.
func capProtocol(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if v := r.Header.Get("MCP-Protocol-Version"); v > mcpProtocolCeiling {
			r.Header.Set("MCP-Protocol-Version", mcpProtocolCeiling)
		}
		if r.Method == http.MethodPost && r.Body != nil {
			body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
			if err != nil {
				http.Error(w, "the request body could not be read", http.StatusBadRequest)
				return
			}
			body = capBody(body)
			r.Body = io.NopCloser(bytes.NewReader(body))
			r.ContentLength = int64(len(body))
		}
		next.ServeHTTP(w, r)
	})
}

func capBody(body []byte) []byte {
	var message map[string]json.RawMessage
	if err := json.Unmarshal(body, &message); err != nil {
		return body
	}
	var method string
	if err := json.Unmarshal(message["method"], &method); err != nil {
		return body
	}
	params := map[string]json.RawMessage{}
	if raw, ok := message["params"]; ok {
		if err := json.Unmarshal(raw, &params); err != nil {
			return body
		}
	}
	changed := method == "initialize" && capVersionField(params, "protocolVersion", true)
	if raw, ok := params["_meta"]; ok {
		meta := map[string]json.RawMessage{}
		if err := json.Unmarshal(raw, &meta); err == nil && capVersionField(meta, metaProtocolVersion, false) {
			if encoded, err := json.Marshal(meta); err == nil {
				params["_meta"] = encoded
				changed = true
			}
		}
	}
	if !changed {
		return body
	}
	encodedParams, err := json.Marshal(params)
	if err != nil {
		return body
	}
	message["params"] = encodedParams
	out, err := json.Marshal(message)
	if err != nil {
		return body
	}
	return out
}

// capVersionField lowers fields[key] to the ceiling when it is newer, and
// when required sets it where it is absent.
func capVersionField(fields map[string]json.RawMessage, key string, required bool) bool {
	var version string
	raw, present := fields[key]
	if present && json.Unmarshal(raw, &version) != nil {
		return false
	}
	if (present && version <= mcpProtocolCeiling) || (!present && !required) {
		return false
	}
	capped, err := json.Marshal(mcpProtocolCeiling)
	if err != nil {
		return false
	}
	fields[key] = capped
	return true
}
