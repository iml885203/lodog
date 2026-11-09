package casecfg

import "path/filepath"

// Names relative to a case directory.
const (
	OpenAPIFile     = "spec/openapi.json"
	OverlayFile     = "spec/overlay.yaml"
	PolicyFile      = "spec/policy.yaml"
	DraftPolicyFile = "spec/policy.auto.yaml"
	WorkflowsFile   = "spec/workflows.arazzo.yaml"
)

// OpenAPI is the legacy system's own document.
func OpenAPI(dir string) string { return filepath.Join(dir, OpenAPIFile) }

// Overlay is the L1 overlay.
func Overlay(dir string) string { return filepath.Join(dir, OverlayFile) }

// Policy is the reviewed L3 policy a config reads when it names none.
func Policy(dir string) string { return filepath.Join(dir, PolicyFile) }

// DraftPolicy is where `lodog policy draft` writes a case's draft.
func DraftPolicy(dir string) string { return filepath.Join(dir, DraftPolicyFile) }

// Workflows is the task tools L2 and L3 serve when a config names none.
func Workflows(dir string) string { return filepath.Join(dir, WorkflowsFile) }
