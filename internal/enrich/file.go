package enrich

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/iml885203/lodog/internal/casecfg"
	"github.com/iml885203/lodog/internal/llm"
	"github.com/iml885203/lodog/internal/policy"
	"github.com/iml885203/lodog/internal/spec"
)

// FileOptions are what `lodog enrich` was asked for.
type FileOptions struct {
	CaseDir     string // reads <case>/spec/openapi.json, writes <case>/spec/overlay.yaml
	SpecPath    string // overrides CaseDir's document
	Out         string // overrides CaseDir's overlay
	PolicyPath  string // policy whose risk levels the model may propose
	Source      string // legacy source directory the model may read
	Model       string
	ClaudeBin   string
	Batch       int
	Concurrency int
	Force       bool
	Exec        llm.Exec // runs the model; llm.Command when nil
}

// RunToFile drafts the overlay for a case and writes it, then reports what it
// wrote and which operations need review to w. It refuses to replace an
// existing overlay without Force, since a person may have reviewed it.
func RunToFile(ctx context.Context, o FileOptions, w io.Writer) error {
	if o.CaseDir != "" {
		if o.SpecPath == "" {
			o.SpecPath = casecfg.OpenAPI(o.CaseDir)
		}
		if o.Out == "" {
			o.Out = casecfg.Overlay(o.CaseDir)
		}
	}
	if o.SpecPath == "" || o.Out == "" {
		return errors.New("give --case, or both --spec and --out")
	}
	if fileExists(o.Out) && !o.Force {
		return fmt.Errorf("%s exists and may hold reviewed edits; pass --force to overwrite", o.Out)
	}
	doc, err := spec.LoadFile(o.SpecPath)
	if err != nil {
		return err
	}
	risks, err := RisksFor(o.CaseDir, o.PolicyPath)
	if err != nil {
		return err
	}
	started := time.Now()
	m := Claude{Claude: llm.Claude{Bin: o.ClaudeBin, Model: o.Model, Exec: o.Exec}}
	res, err := Run(ctx, doc, m, Options{
		SourceDir: o.Source, BatchSize: o.Batch, Concurrency: o.Concurrency,
		Title: doc.Title + " agent overlay", Risks: risks,
	})
	if err != nil {
		return err
	}
	if err := res.Overlay.Save(o.Out); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(w, "wrote %s: %d actions for %d operations in %s\n",
		o.Out, len(res.Overlay.Actions), len(doc.Operations)-len(res.Missing), time.Since(started).Round(time.Second))
	for _, k := range res.Missing {
		_, _ = fmt.Fprintf(w, "  missing: %s\n", k)
	}
	_, _ = fmt.Fprintf(w, "%d operations need review (x-agent.needsReview):\n", len(res.Review))
	for _, k := range res.Review {
		_, _ = fmt.Fprintf(w, "  %s\n", k)
	}
	if len(res.Missing) > 0 {
		return fmt.Errorf("%d operations still have no description after retries (listed above); the overlay is incomplete", len(res.Missing))
	}
	return nil
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// RisksFor reads the policy named by policyPath, else the case's own
// spec/policy.yaml, and falls back to the default levels when there is none.
func RisksFor(caseDir, policyPath string) ([]string, error) {
	if policyPath == "" && caseDir != "" {
		if c := casecfg.Policy(caseDir); fileExists(c) {
			policyPath = c
		}
	}
	if policyPath == "" {
		return RiskVocabulary(nil), nil
	}
	pol, err := policy.LoadFile(policyPath)
	if err != nil {
		return nil, err
	}
	return RiskVocabulary(pol), nil
}
