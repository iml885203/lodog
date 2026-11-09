package report

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"math"
	"sort"
	"strings"

	"github.com/iml885203/lodog/internal/bench/score"
	"github.com/iml885203/lodog/internal/readiness"
)

//go:embed report.html.tmpl
var pageSource string

var page = template.Must(template.New("report").Funcs(template.FuncMap{
	"pts":    func(f float64) string { return fmt.Sprintf("%.2f", f) },
	"pt1":    func(f float64) string { return fmt.Sprintf("%.1f", f) },
	"pct":    func(f float64) string { return fmt.Sprintf("%.0f", f*100) },
	"ticks":  func() []int { return []int{0, 20, 40, 60, 80, 100} },
	"timing": func(s Stage, gateCap float64) Timing { return s.Timing(gateCap) },
	"broke":  func(s Stage, gateCap float64) bool { return s.Broke(gateCap) },
	"arc": func(from, to, r float64) string {
		x1, y1 := gaugePoint(from, r)
		x2, y2 := gaugePoint(to, r)
		return fmt.Sprintf("M %.2f %.2f A %.0f %.0f 0 0 1 %.2f %.2f", x1, y1, r, r, x2, y2)
	},
	"gx":  func(v, r float64) string { x, _ := gaugePoint(v, r); return fmt.Sprintf("%.2f", x) },
	"gy":  func(v, r float64) string { _, y := gaugePoint(v, r); return fmt.Sprintf("%.2f", y) },
	"mul": func(a, b int) int { return a * b },
	// The words of the page: m names a dictionary key with arguments, mm shows
	// a Msg, ma sets an attribute from a key, lvl is a level's name, la a level
	// name as an argument of another sentence, list a list of names.
	"m":   func(key string, args ...any) template.HTML { return spanOf(M(key, args...)) },
	"mm":  spanOf,
	"mt":  func(key string, args ...any) template.HTML { return tspanOf(M(key, args...)) },
	"ma":  func(name, key string, args ...any) template.HTMLAttr { return attrOf(name, M(key, args...)) },
	"mam": attrOf,
	"lvl": func(level string) template.HTML { return spanOf(M("level." + level)) },
	// rd shows a sentence of the static score
	"rd":   func(t readiness.Text) template.HTML { return spanOf(TextMsg(t)) },
	"rdm":  TextMsg,
	"la":   LevelArg,
	"list": func(items []string) string { return "#" + strings.Join(items, ",") },
	"dictJSON": func() (template.JS, error) {
		s, err := dictJSON()
		return template.JS(s), err //nolint:gosec // G203: encoding/json output, which escapes < > and &
	},
	"levelsJSON": func(d Dash) (template.JS, error) {
		b, err := json.Marshal(d.levelsJSON())
		return template.JS(b), err //nolint:gosec // G203: encoding/json output, which escapes < > and &
	},
	"dict": func(kv ...any) map[string]any {
		m := make(map[string]any, len(kv)/2)
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1]
		}
		return m
	},
}).Parse(pageSource))

// TotalMax is a level's full score: static 30 plus dynamic 70.
const TotalMax = 100

// page is what the template reads: the report plus the tables it derives.
type pageData struct {
	Report
	Story     Story
	Checks    []checkRow
	ByType    []typeRow
	TaskRows  []taskRow
	RunLevels []Level
}

type checkRow struct {
	Label Msg
	Max   float64
	Cells []readiness.Check
}

type taskRow struct {
	ID    string
	Type  score.TaskType
	Cells []*score.TaskResult
}

type typeRow struct {
	Type     score.TaskType
	Category score.Category
	Cells    []*score.TypeResult
}

// RenderHTML writes the report as one self-contained HTML page: its style and
// its one small script are inline and it loads no font, stylesheet or image,
// so it opens offline from a file or an attachment as it is. The animation
// only runs when the script arms it; without the script, or with
// prefers-reduced-motion, the page shows the end state.
func RenderHTML(w io.Writer, r Report) error {
	d := pageData{Report: r, Story: BuildStory(r), Checks: checkRows(r.Levels)}
	for _, l := range r.Levels {
		if l.Run != nil {
			d.RunLevels = append(d.RunLevels, l)
		}
	}
	d.ByType = typeRows(d.RunLevels)
	d.TaskRows = taskRows(d.RunLevels)
	if err := page.Execute(w, d); err != nil {
		return fmt.Errorf("render report: %w", err)
	}
	return nil
}

// checkRows lines the static checks up by id; every level is scored by the
// same checks, so the first level's order is every level's order.
func checkRows(levels []Level) []checkRow {
	if len(levels) == 0 {
		return nil
	}
	var rows []checkRow
	for _, c := range levels[0].Static.Checks {
		row := checkRow{Label: TextMsg(c.Name), Max: c.Max}
		for _, l := range levels {
			row.Cells = append(row.Cells, findCheck(l.Static.Checks, c.ID))
		}
		rows = append(rows, row)
	}
	return rows
}

func findCheck(checks []readiness.Check, id string) readiness.Check {
	for _, c := range checks {
		if c.ID == id {
			return c
		}
	}
	return readiness.Check{ID: id}
}

func typeRows(levels []Level) []typeRow {
	rows := make([]typeRow, 0, len(score.TaskTypes))
	for _, info := range score.TaskTypes {
		row := typeRow{Type: info.Type, Category: info.Category}
		for _, l := range levels {
			row.Cells = append(row.Cells, findType(l.Run.Score.ByType, info.Type))
		}
		rows = append(rows, row)
	}
	return rows
}

// taskRows lines every task any run scored up across the levels' runs.
func taskRows(levels []Level) []taskRow {
	var rows []taskRow
	seen := map[string]bool{}
	for _, l := range levels {
		for _, t := range l.Run.Score.Tasks {
			if !seen[t.ID] {
				seen[t.ID] = true
				rows = append(rows, taskRow{ID: t.ID, Type: t.Type})
			}
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	for i := range rows {
		for _, l := range levels {
			rows[i].Cells = append(rows[i].Cells, findTask(l.Run.Score.Tasks, rows[i].ID))
		}
	}
	return rows
}

func findTask(results []score.TaskResult, id string) *score.TaskResult {
	for i := range results {
		if results[i].ID == id {
			return &results[i]
		}
	}
	return nil
}

func findType(results []score.TypeResult, t score.TaskType) *score.TypeResult {
	for i := range results {
		if results[i].Type == t {
			return &results[i]
		}
	}
	return nil
}

// gaugePoint is the point at score v on a circle of radius r around the
// dial's centre: 0 at the left, 100 at the right, over the top.
func gaugePoint(v, r float64) (x, y float64) {
	a := math.Pi * (1 - v/100)
	return 200 + r*math.Cos(a), 240 - r*math.Sin(a)
}
