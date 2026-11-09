package review

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/iml885203/lodog/internal/casecfg"

	"github.com/iml885203/lodog/internal/outfile"
)

// DraftPolicy is where docs/policy.md has `lodog policy draft` write a case's
// draft.
const DraftPolicy = casecfg.DraftPolicyFile

// ReviewLog is the review log's file name, next to the checklist.
const ReviewLog = "review-log.yaml"

// AskOptions are what `lodog review ask` was asked for.
type AskOptions struct {
	CaseDir string // fills in the drafts Files leaves out, where they exist
	Files   Files
	Out     string // the checklist; empty = stdout
	HTML    string // also write a one-page HTML summary here
}

// AskToFiles groups the drafts' pending items into a checklist and writes it
// to Out (or stdout) and, with HTML, a page; it reports the counts to stderr.
func AskToFiles(o AskOptions, stdout, stderr io.Writer) error {
	files := o.Files
	if o.CaseDir != "" {
		files = CaseFiles(o.CaseDir, files)
	}
	files, err := files.absolute()
	if err != nil {
		return err
	}
	c, err := Ask(files)
	if err != nil {
		return err
	}
	data, err := c.Marshal()
	if err != nil {
		return err
	}
	if err := outfile.Write(stdout, o.Out, data); err != nil {
		return err
	}
	if o.HTML != "" {
		if err := writeHTML(o.HTML, c, o.Out); err != nil {
			return err
		}
	}
	items := 0
	for _, q := range c.Questions {
		items += len(q.Items)
	}
	_, err = fmt.Fprintf(stderr, "%d questions about %d pending items\n", len(c.Questions), items)
	return err
}

// absolute names the drafts by full path: the checklist is applied from
// wherever its owner stands, not from where it was asked.
func (f Files) absolute() (Files, error) {
	for _, p := range []*string{&f.Policy, &f.Overlay, &f.Workflows} {
		if *p == "" {
			continue
		}
		abs, err := filepath.Abs(*p)
		if err != nil {
			return f, fmt.Errorf("full path of %s: %w", *p, err)
		}
		*p = abs
	}
	return f, nil
}

// CaseFiles fills in the case's drafts the given files left out, where they exist.
func CaseFiles(caseDir string, files Files) Files {
	cfg := &casecfg.Config{Case: caseDir}
	pick := func(set, path string) string {
		if set != "" {
			return set
		}
		if _, err := os.Stat(path); err != nil {
			return ""
		}
		return path
	}
	policyPath := pick(files.Policy, filepath.Join(caseDir, DraftPolicy))
	if policyPath == "" {
		policyPath = pick("", cfg.PolicyPath())
	}
	return Files{
		Policy:    policyPath,
		Overlay:   pick(files.Overlay, cfg.OverlayPath()),
		Workflows: pick(files.Workflows, cfg.WorkflowsPath()),
	}
}

func writeHTML(path string, c *Checklist, checklist string) error {
	opts := PageOptions{Checklist: checklist}
	if checklist != "" {
		log, err := LoadLog(filepath.Join(filepath.Dir(checklist), ReviewLog))
		if err != nil {
			return err
		}
		opts.Log = log
	}
	f, err := os.Create(path) //nolint:gosec // G304: the operator names the output file
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	if err := RenderHTML(f, c, opts); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// ApplyOptions are what `lodog review apply` was asked for.
type ApplyOptions struct {
	By        string
	PolicyOut string
	LogPath   string // default review-log.yaml next to the checklist
	Now       time.Time
}

// ApplyFile applies the answers of the checklist at path and appends them to
// the review log, which it writes even when a later answer is refused so the
// ones that were applied are on record. It reports each answer to w.
func ApplyFile(path string, o ApplyOptions, w io.Writer) error {
	c, err := Load(path)
	if err != nil {
		return err
	}
	logPath := o.LogPath
	if logPath == "" {
		logPath = filepath.Join(filepath.Dir(path), ReviewLog)
	}
	entries, err := Apply(c, Options{By: o.By, Now: o.Now, PolicyOut: o.PolicyOut})
	if len(entries) > 0 {
		if logErr := AppendLog(logPath, entries); logErr != nil && err == nil {
			err = logErr
		}
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if _, err := fmt.Fprintf(w, "%s: %d items, %d changed -> %s\n", e.Question, e.Items, len(e.Changed), e.Wrote); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(w, "logged %d answers to %s\n", len(entries), logPath)
	return err
}
