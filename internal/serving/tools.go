// Package serving is the one answer to "what does the gateway serve": the
// tools an agent sees for a level (one per operation at L0 and L1, one per
// workflow at L2 and L3), the operations it does not serve with the reason, and
// which tools the L3 policy hides. lodog serve, score and init all read it, so
// what is scored is what is served. It knows nothing of MCP transport,
// approvals or the upstream session.
package serving

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"

	"github.com/iml885203/lodog/internal/spec"
)

// BodyArg is the tool argument that carries the JSON request body, kept apart
// from path and query parameters so a body field can never shadow one.
const BodyArg = "requestBody"

// MaxToolName is the longest tool name clients accept (Claude: 64).
const MaxToolName = 64

// RawTool is one legacy operation served one-to-one as an MCP tool: the L0
// and L1 shape. Nothing is curated, grouped or hidden.
type RawTool struct {
	Tool Tool
	Op   spec.Operation
}

// Tool is what an agent is shown of one served tool. The gateway turns it into
// an MCP tool; lodog score reads it as it is, so neither needs the other's
// transport.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"inputSchema"`
}

// Skipped is an operation the gateway does not serve, with the reason.
type Skipped struct {
	Op     spec.Operation
	Reason string
}

// RawTools turns every operation into a tool. An operation the gateway cannot
// forward faithfully is skipped and reported, never served half-working.
func RawTools(doc *spec.Document) ([]RawTool, []Skipped) {
	var tools []RawTool
	var skipped []Skipped
	used := map[string]bool{}
	for _, op := range doc.Operations {
		if reason := unservable(op); reason != "" {
			skipped = append(skipped, Skipped{Op: op, Reason: reason})
			continue
		}
		name := uniqueName(toolName(op), used)
		tools = append(tools, RawTool{
			Op: op,
			Tool: Tool{
				Name:        name,
				Description: rawDescription(op),
				InputSchema: inputSchema(op),
			},
		})
	}
	return tools, skipped
}

// rawDescription is what an agent gets for a one-to-one tool: the method and
// path, plus whatever the document says. At L0 that is the document as is
// (for Backoffice, nothing more); at L1 the overlay has filled in the summary,
// description and when to use the operation.
func rawDescription(op spec.Operation) string {
	parts := []string{op.Method + " " + op.Path}
	for _, s := range []string{op.Summary, op.Description} {
		if s != "" {
			parts = append(parts, s)
		}
	}
	if op.WhenToUse != "" {
		parts = append(parts, "Use when: "+op.WhenToUse)
	}
	if op.WhenNotToUse != "" {
		parts = append(parts, "Do not use when: "+op.WhenNotToUse)
	}
	return strings.Join(parts, "\n")
}

func inputSchema(op spec.Operation) map[string]any {
	props := map[string]any{}
	var required []string
	for _, p := range op.Params {
		schema := map[string]any{}
		for k, v := range p.Schema {
			schema[k] = v
		}
		if p.Description != "" {
			// The parameter's own description replaces the schema's, which
			// is where the allowed values were written.
			schema["description"] = p.Description
			if sentence := spec.EnumDescription(p.Schema); sentence != "" && !strings.Contains(p.Description, sentence) {
				schema["description"] = spec.AppendSentence(p.Description, sentence)
			}
		}
		props[p.ArgName()] = schema
		if p.Required {
			required = append(required, p.ArgName())
		}
	}
	if op.Body != nil {
		props[BodyArg] = bodySchema(op)
		if op.BodyRequired {
			required = append(required, BodyArg)
		}
	}
	schema := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

var (
	nonName  = regexp.MustCompile(`[^a-zA-Z0-9_]+`)
	repeated = regexp.MustCompile(`_+`)
)

// toolName prefers the document's operationId; Backoffice has none, so the name
// is built from the method and path: GET /api/customer/{customerId} becomes
// get_api_customer_by_customerId.
func toolName(op spec.Operation) string {
	base := op.OperationID
	if base == "" {
		segments := []string{strings.ToLower(op.Method)}
		for _, seg := range strings.Split(strings.Trim(op.Path, "/"), "/") {
			if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
				seg = "by_" + strings.Trim(seg, "{}")
			}
			segments = append(segments, seg)
		}
		base = strings.Join(segments, "_")
	}
	name := strings.Trim(repeated.ReplaceAllString(nonName.ReplaceAllString(base, "_"), "_"), "_")
	if len(name) <= MaxToolName {
		return name
	}
	sum := sha256.Sum256([]byte(op.Method + " " + op.Path))
	suffix := "_" + hex.EncodeToString(sum[:])[:8]
	return name[:MaxToolName-len(suffix)] + suffix
}

func uniqueName(name string, used map[string]bool) string {
	candidate := name
	for i := 2; used[candidate]; i++ {
		suffix := fmt.Sprintf("_%d", i)
		if len(name)+len(suffix) > MaxToolName {
			candidate = name[:MaxToolName-len(suffix)] + suffix
		} else {
			candidate = name + suffix
		}
	}
	used[candidate] = true
	return candidate
}
