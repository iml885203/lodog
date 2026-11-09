package review

import (
	_ "embed"
	"fmt"
	"html/template"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/iml885203/lodog/internal/policy"
)

//go:embed review.html.tmpl
var pageSource string

var page = template.Must(template.New("review").Funcs(template.FuncMap{
	"t":    span,
	"dict": dictJSON,
}).Parse(pageSource))

// PageOptions say where the page's answers go and what has been answered.
type PageOptions struct {
	// Checklist is the path of the checklist file the page was made from, the
	// file a person edits to answer; empty when it went to stdout.
	Checklist string
	// Log is the review log, whose entries the page reports as applied.
	Log []Entry
}

// RenderHTML writes the checklist as one self-contained page a person can
// review from: how to answer, then every question in plain words with its
// items folded away. The page is read-only; answers go in the checklist.
func RenderHTML(w io.Writer, c *Checklist, o PageOptions) error {
	if err := page.Execute(w, newPageData(c, o)); err != nil {
		return fmt.Errorf("render review: %w", err)
	}
	return nil
}

type pageData struct {
	*Checklist
	Items     int
	Questions []questionView
	Applied   template.HTML // empty when nothing was applied yet
	FirstID   string        // the first question, which the example answers
	Path      string        // the checklist file, as a person opens it
	Example   template.HTML // the answer lines to copy
	Choices   string        // the first question's choices, for the example's <choice>
	Command   string        // the apply command to copy
}

type questionView struct {
	Question
	Rows     []row
	Answered template.HTML
}

type row struct {
	Label, Text template.HTML
}

func newPageData(c *Checklist, o PageOptions) pageData {
	d := pageData{Checklist: c, Path: o.Checklist}
	if d.Path != "" {
		if abs, err := filepath.Abs(d.Path); err == nil {
			d.Path = abs
		}
	}
	risks := policyRisks(c.Files.Policy)
	for _, q := range c.Questions {
		d.Items += len(q.Items)
		d.Questions = append(d.Questions, viewOf(q, risks))
	}
	d.FirstID = "<question id>"
	if len(c.Questions) > 0 {
		d.FirstID = c.Questions[0].ID
	}
	d.Applied = appliedOf(o.Log)
	d.Example, d.Command = example(c, d.Path)
	if len(c.Questions) > 0 {
		d.Choices = strings.Join(c.Questions[0].Choices, ", ")
	}
	return d
}

// example is the answer lines a person copies and the command that applies
// them, written with the checklist's own first question and file. The lines
// sit where they go in the file: under the question's own id, indented like
// the checklist, with the comments in the page's language.
func example(c *Checklist, path string) (answer template.HTML, command string) {
	id, item := "<question id>", "<item>"
	if len(c.Questions) > 0 {
		q := c.Questions[0]
		id = q.ID
		if len(q.Items) > 0 {
			item = q.Items[0].Key
		}
	}
	file := path
	if file == "" {
		file = "review.yaml"
	}
	esc := template.HTMLEscapeString
	block := func(comment, line string) string {
		return string(span(comment)) + "\nquestions:\n  - id: " + esc(id) + "\n" +
			"    " + string(span("ex.rest")) + "\n" +
			"    answer: " + esc(line)
	}
	q := strconv.Quote(item)
	answer = template.HTML(strings.Join([]string{ //nolint:gosec // G203: every dynamic part is escaped
		block("ex.yes", "{confirmed: true}"),
		block("ex.except", "{confirmed: true, change: {"+q+": <choice>}}"),
		block("ex.no", "{confirmed: false, change: {"+q+": <choice>, ...}}"),
	}, "\n\n"))
	command = "lodog review apply " + shellQuote(file) + ` --by "<your name>"`
	return answer, command
}

func shellQuote(s string) string {
	if strings.ContainsAny(s, " \t'\"$`\\") {
		return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
	}
	return s
}

// policyRisks reads the risks of the policy the questions were asked about;
// a policy that cannot be read leaves the effects unnamed.
func policyRisks(path string) map[string]policy.Risk {
	if path == "" {
		return nil
	}
	p, err := policy.LoadFile(path)
	if err != nil {
		return nil
	}
	return p.Risks
}

func effectKey(r policy.Risk, ok bool) string {
	switch {
	case !ok:
		return "effect.unknown"
	case r.Action == policy.Approval && r.NeverAutoApprove:
		return "effect.approval_never_auto"
	case r.Action == policy.Allow, r.Action == policy.Approval, r.Action == policy.Deny, r.Action == policy.Hide:
		return "effect." + string(r.Action)
	}
	return "effect.unknown"
}

// kindOf is the sentence set a question is worded from.
func kindOf(q Question) string {
	switch {
	case q.Source == SourcePolicy && strings.HasSuffix(q.ID, "-unsure"):
		return "unsure"
	case q.Source == SourcePolicy:
		return "policy"
	case q.Source == SourceOverlay && q.Proposed == unsetRisk:
		return "unset"
	case q.Source == SourceOverlay:
		return "overlay"
	case q.Source == SourceWorkflows:
		return "workflows"
	}
	return "other"
}

// viewOf says what a question asks in plain words: what is decided, what
// lodog suggests and what a yes and a no each do. The sentence is chosen by the
// question's source and id; a question of a kind this page does not know is
// shown with its own ask and why.
func viewOf(q Question, risks map[string]policy.Risk) questionView {
	n, risk := strconv.Itoa(len(q.Items)), q.Proposed
	choices := strings.Join(q.Choices, ", ")
	args := []string{n, risk, "", choices}
	kind := kindOf(q)
	if kind == "policy" || kind == "unsure" {
		r, ok := risks[q.Proposed]
		args[2] = "@" + effectKey(r, ok)
	}
	yes, no := kind+".yes", kind+".no"
	if kind == "unsure" {
		yes, no = "policy.yes", "policy.no"
	}
	if kind == "overlay" && q.Proposed == "read" {
		yes = "overlay.yes.read"
	}
	v := questionView{Question: q}
	decide := span(kind+".decide", args...)
	if kind == "other" {
		decide = template.HTML(template.HTMLEscapeString(q.Ask)) //nolint:gosec // G203: escaped here
	}
	v.Rows = []row{
		{span("q.decide"), decide},
		{span("q.suggest"), span(kind+".suggest", args...)},
		{span("q.yes"), span(yes, args...)},
		{span("q.no"), span(no, args...)},
	}
	if kind == "other" {
		v.Rows[1].Text = span("other.suggest")
	}
	if _, ok := zh["risk."+risk]; ok && kind != "workflows" && kind != "other" {
		v.Rows = append(v.Rows, row{span("q.risk", risk), span("risk." + risk)})
	}
	if a := q.Answer; a != nil {
		verdict := "ans.no"
		if a.Confirmed {
			verdict = "ans.yes"
		}
		v.Answered = span("q.answered", "@"+verdict)
	}
	return v
}

// appliedOf is the line about what the review log records: who answered how
// many questions and when. Empty when the log has no entries.
func appliedOf(log []Entry) template.HTML {
	if len(log) == 0 {
		return ""
	}
	var by []string
	last := log[0].At
	for _, e := range log {
		if !contains(by, e.By) {
			by = append(by, e.By)
		}
		if e.At.After(last) {
			last = e.At
		}
	}
	return span("applied", strings.Join(by, ", "), last.UTC().Format("2006-01-02 15:04 UTC"), strconv.Itoa(len(log)))
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// LoadLog reads the review log at path; a log that does not exist yet is empty.
func LoadLog(path string) ([]Entry, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: the operator names their own review log
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read review log: %w", err)
	}
	var log []Entry
	if err := yaml.Unmarshal(data, &log); err != nil {
		return nil, fmt.Errorf("parse review log %s: %w", path, err)
	}
	return log, nil
}
