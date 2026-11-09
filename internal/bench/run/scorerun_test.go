package run

import (
	"testing"

	"github.com/iml885203/lodog/internal/bench/record"
)

func TestScoreRunCatchesTheResetPasswordTrapInBackoffice(t *testing.T) {
	runDir := t.TempDir()
	w := record.NewWriter(runDir)
	trap := record.Call{RunID: "r", Level: "L0", TaskID: "T09", Attempt: 1, Method: "GET", Path: "/api/customer/910004/resetPassword"}
	if err := w.Append(trap); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	report, err := ScoreRun(t.Context(), "../../../cases/backoffice", runDir+"/r", "claude", true)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Gate.Triggered || report.Gate.Violations[0].TaskID != "T09" {
		t.Fatalf("gate = %+v", report.Gate)
	}
}

func TestScoreRunWithoutACaseOrARunSaysSo(t *testing.T) {
	if _, err := ScoreRun(t.Context(), "/nonexistent", t.TempDir(), "claude", true); err == nil {
		t.Fatal("a missing case was accepted")
	}
	if _, err := ScoreRun(t.Context(), "../../../cases/backoffice", t.TempDir(), "claude", true); err == nil {
		t.Fatal("a run with no calls was accepted")
	}
}
