package workflowdraft

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/iml885203/lodog/internal/casecfg"

	"github.com/iml885203/lodog/internal/llm"
	"github.com/iml885203/lodog/internal/spec"
)

// FileOptions are what `lodog workflows draft` was asked for.
type FileOptions struct {
	CaseDir   string // reads the case's OpenAPI document and overlay
	Out       string // default <case>/casecfg.DefaultWorkflows
	Source    string // the sourceDescriptions name the workflows call into
	Model     string
	ClaudeBin string
	Force     bool
	Exec      llm.Exec // runs the model; llm.Command when nil
}

// RunToFile drafts the task tools of a case and writes the Arazzo document
// with a header telling a person to review it, then reports what it wrote to
// w. It refuses to replace an existing file without Force, since a person may
// have reviewed it.
func RunToFile(ctx context.Context, o FileOptions, w io.Writer) error {
	if o.CaseDir == "" {
		return errors.New("give --case")
	}
	if o.Out == "" {
		o.Out = casecfg.Workflows(o.CaseDir)
	}
	if _, err := os.Stat(o.Out); err == nil && !o.Force {
		return fmt.Errorf("%s exists and may hold reviewed edits; pass --force to overwrite", o.Out)
	}
	cfg := &casecfg.Config{Case: o.CaseDir}
	doc, err := spec.LoadFileWithOverlay(cfg.OpenAPIPath(), cfg.OverlayPath())
	if err != nil {
		return err
	}
	asker, cleanup, err := llm.Isolated(llm.Claude{Bin: o.ClaudeBin, Model: o.Model, Exec: o.Exec}, "lodog-workflows-draft-")
	if err != nil {
		return err
	}
	defer cleanup()
	d, err := Run(ctx, doc, asker, Options{
		Title: doc.Title + " task tools", Source: o.Source, URL: "openapi.json",
	})
	if err != nil {
		return err
	}
	data, err := d.Document.Marshal()
	if err != nil {
		return err
	}
	header := fmt.Sprintf("# Drafted by `lodog workflows draft` from %s with %s; model %s.\n"+
		"# A draft for a person to review before an L2 or L3 gateway serves it (docs/levels.md):\n"+
		"# check every needsReview workflow, the inputs and the fixed parameter values.\n",
		cfg.OpenAPIPath(), cfg.OverlayPath(), o.Model)
	if err := os.WriteFile(o.Out, append([]byte(header), data...), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", o.Out, err)
	}
	_, _ = fmt.Fprintf(w, "wrote %s: %d task tools, %d need review\n", o.Out, len(d.Document.Workflows), len(d.Review))
	for _, n := range d.Review {
		_, _ = fmt.Fprintf(w, "  review: %s\n", n)
	}
	for _, n := range d.Unreversed {
		_, _ = fmt.Fprintf(w, "  no undo: %s\n", n)
	}
	for _, n := range d.Dropped {
		_, _ = fmt.Fprintf(w, "  dropped: %s\n", n)
	}
	return nil
}
