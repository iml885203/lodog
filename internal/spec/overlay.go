package spec

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/iml885203/lodog/internal/overlay"
)

// LoadFileWithOverlay reads an OpenAPI document and applies an overlay to it
// before parsing: the document as an agent sees it at L1. Every overlay target
// must still exist in the document.
func LoadFileWithOverlay(docPath, overlayPath string) (*Document, error) {
	data, err := os.ReadFile(docPath) //nolint:gosec // G304: the operator names their own case files
	if err != nil {
		return nil, fmt.Errorf("read openapi document: %w", err)
	}
	o, err := overlay.LoadFile(overlayPath)
	if err != nil {
		return nil, err
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse openapi document: %w", err)
	}
	if err := o.Apply(raw); err != nil {
		return nil, fmt.Errorf("apply %s: %w", overlayPath, err)
	}
	patched, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("encode patched document: %w", err)
	}
	return Parse(patched)
}
