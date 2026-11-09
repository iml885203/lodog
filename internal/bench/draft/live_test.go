package draft

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iml885203/lodog/internal/bench/score"
)

func TestRecordTasksWritesOneFilePerTaskAndPrintsItsPath(t *testing.T) {
	dir := t.TempDir()
	c := score.Case{Dir: dir, Tasks: map[string]score.Task{
		"A": {ID: "A", Calls: []string{"GET /api/a"}},
		"B": {ID: "B", Calls: []string{"GET /api/b"}},
	}}
	var out bytes.Buffer
	if err := recordTasks(t.Context(), &out, c, &fakeReader{body: `{"ok":1}`}, []string{"A", "B"}); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "expected", "raw", "A.json") + "\n" + filepath.Join(dir, "expected", "raw", "B.json") + "\n"
	if out.String() != want {
		t.Fatalf("printed %q, want %q", out.String(), want)
	}
	got, err := os.ReadFile(filepath.Join(dir, "expected", "raw", "A.json")) //nolint:gosec // G304: a test temp path
	if err != nil || !strings.Contains(string(got), `"call": "GET /api/a"`) {
		t.Fatalf("A.json = %s, err %v", got, err)
	}
}

func TestRecordTasksRefusesATaskTheBankDoesNotHave(t *testing.T) {
	c := score.Case{Dir: t.TempDir(), Tasks: map[string]score.Task{}}
	err := recordTasks(t.Context(), &bytes.Buffer{}, c, &fakeReader{}, []string{"X9"})
	if err == nil || !strings.Contains(err.Error(), "unknown task X9") {
		t.Fatalf("err = %v", err)
	}
}

func TestLiveSessionReportsAFailedReset(t *testing.T) {
	_, err := LiveSession(t.Context(), score.Case{Reset: []string{"/nonexistent/reset"}}, "../../../cases/backoffice/gateway/L0.yaml")
	if err == nil || !strings.Contains(err.Error(), "reset:") {
		t.Fatalf("err = %v, want the reset failure", err)
	}
}

func TestLiveSessionNeedsAReadableGatewayConfig(t *testing.T) {
	if _, err := LiveSession(t.Context(), score.Case{}, "/nonexistent/gw.yaml"); err == nil {
		t.Fatal("a missing gateway config was accepted")
	}
}

func TestHasErrorIgnoresWarnings(t *testing.T) {
	if HasError([]Finding{{Severity: SeverityWarning}}) || !HasError([]Finding{{Severity: SeverityWarning}, {Severity: SeverityError}}) {
		t.Fatal("only an error finding should count")
	}
}

func TestCheckCasePassesTheBackofficeBankWithoutALiveSystem(t *testing.T) {
	findings, err := CheckCase(t.Context(), "../../../cases/backoffice", "")
	if err != nil || HasError(findings) {
		t.Fatalf("findings %v, err %v", findings, err)
	}
}

func TestScanFileProposesEndpointsFromADocument(t *testing.T) {
	r, err := ScanFile("../../../cases/backoffice/spec/openapi.json", "", 3)
	if err != nil || len(r.Types) == 0 {
		t.Fatalf("scan = %+v, err %v", r, err)
	}
	if _, err := ScanFile("/nonexistent.json", "", 3); err == nil {
		t.Fatal("a missing document was accepted")
	}
}
