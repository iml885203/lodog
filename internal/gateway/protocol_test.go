package gateway

import (
	"encoding/json"
	"testing"
)

func TestANewerClientIsHeldAtTheCeiling(t *testing.T) {
	out := capBody([]byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2026-07-28"}}`))
	var msg struct {
		Params struct {
			ProtocolVersion string `json:"protocolVersion"`
		} `json:"params"`
	}
	if err := json.Unmarshal(out, &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Params.ProtocolVersion != mcpProtocolCeiling {
		t.Fatalf("protocolVersion = %q, want %q", msg.Params.ProtocolVersion, mcpProtocolCeiling)
	}
}

func TestAVersionStatedOnlyInMetaIsCapped(t *testing.T) {
	out := capBody([]byte(`{"method":"tools/call","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}`))
	var msg struct {
		Params struct {
			Meta map[string]string `json:"_meta"`
		} `json:"params"`
	}
	if err := json.Unmarshal(out, &msg); err != nil {
		t.Fatal(err)
	}
	if got := msg.Params.Meta[metaProtocolVersion]; got != mcpProtocolCeiling {
		t.Fatalf("meta version = %q", got)
	}
}

func TestAnOlderClientIsLeftAlone(t *testing.T) {
	in := `{"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`
	if out := string(capBody([]byte(in))); out != in {
		t.Fatalf("body changed: %s", out)
	}
}
