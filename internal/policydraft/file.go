package policydraft

import (
	"context"
	"fmt"
	"io"

	"github.com/iml885203/lodog/internal/llm"
	"github.com/iml885203/lodog/internal/outfile"
	"github.com/iml885203/lodog/internal/readiness"
	"github.com/iml885203/lodog/internal/spec"
)

// FileOptions are what `lodog policy draft` was asked for.
type FileOptions struct {
	OpenAPI   string // the OpenAPI document to classify
	CaseDir   string // adds the system's own signal words
	RisksPath string // the system's own risk set instead of the default
	Out       string // empty = stdout
	Model     string
	ClaudeBin string
	Exec      llm.Exec // runs the model; llm.Command when nil
}

// RunToFile drafts the policy of an OpenAPI document and writes it to Out (or
// stdout), then writes the summary and the policy's warnings to stderr.
func RunToFile(ctx context.Context, o FileOptions, stdout, stderr io.Writer) error {
	doc, err := spec.LoadFile(o.OpenAPI)
	if err != nil {
		return err
	}
	sig, err := readiness.SignalsOf(o.CaseDir)
	if err != nil {
		return err
	}
	rs := DefaultRiskSet()
	if o.RisksPath != "" {
		if rs, err = LoadRiskSet(o.RisksPath); err != nil {
			return err
		}
	}
	asker, cleanup, err := llm.Isolated(llm.Claude{Bin: o.ClaudeBin, Model: o.Model, Exec: o.Exec}, "lodog-policy-draft-")
	if err != nil {
		return err
	}
	defer cleanup()
	cs, err := Classify(ctx, doc, asker, sig, rs)
	if err != nil {
		return err
	}
	d := Draft{Source: o.OpenAPI, Model: o.Model, Risks: rs, Classifications: cs}
	data, err := d.Marshal()
	if err != nil {
		return err
	}
	if err := outfile.Write(stdout, o.Out, data); err != nil {
		return err
	}
	if err := d.Summary().Render(stderr); err != nil {
		return err
	}
	p := d.Policy()
	for _, w := range p.Warnings() {
		if _, err := fmt.Fprintln(stderr, "warning: "+w); err != nil {
			return err
		}
	}
	return nil
}
