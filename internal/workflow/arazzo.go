// Package workflow reads the task tools of L2 and L3: an Arazzo 1.0 document
// (spec/workflows.arazzo.yaml) whose workflows each become one tool, checks it
// against the legacy OpenAPI document, and runs a workflow's steps through a
// caller the gateway supplies. It knows nothing about MCP or HTTP transport.
//
// lodog reads a subset of Arazzo 1.0 (docs/contracts.md §3): one OpenAPI
// source, steps that name an operation, parameters and request bodies whose
// values are literals or whole runtime expressions, and step and workflow
// outputs. A step succeeds on a 2xx status; successCriteria, onSuccess,
// onFailure, components and dependsOn are not read, and a document that uses
// them is refused rather than run differently from what it says.
package workflow

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Document is an Arazzo document as lodog reads it.
type Document struct {
	Arazzo             string     `yaml:"arazzo"`
	Info               Info       `yaml:"info"`
	SourceDescriptions []Source   `yaml:"sourceDescriptions,omitempty"`
	Workflows          []Workflow `yaml:"workflows"`
}

// Info describes the document. Description is what the gateway gives the
// agent as its server instructions: the map of the domains and the usual
// order of the tools.
type Info struct {
	Title       string `yaml:"title"`
	Version     string `yaml:"version"`
	Description string `yaml:"description,omitempty"`
}

// Source is the OpenAPI document the steps call into.
type Source struct {
	Name string `yaml:"name"`
	URL  string `yaml:"url,omitempty"`
	Type string `yaml:"type,omitempty"`
}

// Workflow is one task tool.
type Workflow struct {
	WorkflowID  string `yaml:"workflowId"`
	Summary     string `yaml:"summary,omitempty"`
	Description string `yaml:"description,omitempty"`
	// Inputs is a JSON schema of type object: the tool's arguments.
	Inputs  map[string]any    `yaml:"inputs,omitempty"`
	Steps   []Step            `yaml:"steps"`
	Outputs map[string]string `yaml:"outputs,omitempty"`
	Agent   Agent             `yaml:"x-agent,omitempty"`

	SuccessActions any `yaml:"successActions,omitempty"`
	FailureActions any `yaml:"failureActions,omitempty"`
	DependsOn      any `yaml:"dependsOn,omitempty"`
	Parameters     any `yaml:"parameters,omitempty"`
}

// Agent is lodog's x-agent extension on a workflow.
type Agent struct {
	WhenToUse    string `yaml:"whenToUse,omitempty"`
	WhenNotToUse string `yaml:"whenNotToUse,omitempty"`
	// Risk is the risk the tool carries for the agent's reading. It does not
	// decide enforcement: at L3 the policy decides every step's operation.
	Risk string `yaml:"risk,omitempty"`
	// Next names the tools an agent usually calls after this one.
	Next []string `yaml:"next,omitempty"`
	// NeedsReview marks a drafted workflow a person has not reviewed yet.
	NeedsReview bool `yaml:"needsReview,omitempty"`
	// ReviewNote is what the person reviewing is asked or told about a tool
	// marked needsReview. The agent never sees it.
	ReviewNote string `yaml:"reviewNote,omitempty"`
}

// Step calls one operation.
type Step struct {
	StepID        string            `yaml:"stepId"`
	Description   string            `yaml:"description,omitempty"`
	OperationID   string            `yaml:"operationId,omitempty"`
	OperationPath string            `yaml:"operationPath,omitempty"`
	Parameters    []Parameter       `yaml:"parameters,omitempty"`
	RequestBody   *RequestBody      `yaml:"requestBody,omitempty"`
	Outputs       map[string]string `yaml:"outputs,omitempty"`

	WorkflowID      string `yaml:"workflowId,omitempty"`
	SuccessCriteria any    `yaml:"successCriteria,omitempty"`
	OnSuccess       any    `yaml:"onSuccess,omitempty"`
	OnFailure       any    `yaml:"onFailure,omitempty"`
}

// Parameter is one operation parameter and the value it gets.
type Parameter struct {
	Name  string `yaml:"name"`
	In    string `yaml:"in,omitempty"`
	Value any    `yaml:"value"`
	Ref   string `yaml:"reference,omitempty"`
}

// RequestBody is the JSON body a step sends.
type RequestBody struct {
	ContentType  string `yaml:"contentType,omitempty"`
	Payload      any    `yaml:"payload,omitempty"`
	Replacements any    `yaml:"replacements,omitempty"`
}

// LoadFile reads an Arazzo document. It is not checked against an OpenAPI
// document yet: call Bind for that.
func LoadFile(path string) (*Document, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: the operator names their own case files
	if err != nil {
		return nil, fmt.Errorf("read workflows: %w", err)
	}
	d, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("workflows %s: %w", path, err)
	}
	return d, nil
}

// Parse reads an Arazzo document from YAML or JSON.
func Parse(data []byte) (*Document, error) {
	var d Document
	if err := yaml.Unmarshal(data, &d); err != nil {
		return nil, fmt.Errorf("parse workflows: %w", err)
	}
	return &d, nil
}

// Marshal writes the document as YAML.
func (d *Document) Marshal() ([]byte, error) {
	data, err := yaml.Marshal(d)
	if err != nil {
		return nil, fmt.Errorf("encode workflows: %w", err)
	}
	return data, nil
}
