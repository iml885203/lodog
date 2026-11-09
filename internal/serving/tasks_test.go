package serving

import (
	"strings"
	"testing"

	"github.com/iml885203/lodog/internal/casecfg"
)

func TestAMissingWorkflowsFileNamesTheCommandThatDraftsIt(t *testing.T) {
	cfg := &casecfg.Config{Case: t.TempDir(), Level: casecfg.L2}
	if _, err := LoadTasks(cfg, nil, nil); err == nil || !strings.Contains(err.Error(), "lodog workflows draft --case "+cfg.Case) {
		t.Fatalf("err = %v", err)
	}
}

// The Backoffice task tools must serve every operation the benchmark's tasks
// read or write through (cases/backoffice/expected/answers.yaml), except the
// side-effecting GET that L2 leaves out on purpose.
func TestTheBackofficeTaskToolsCoverTheBenchmarkOperations(t *testing.T) {
	cfg, err := casecfg.LoadConfig("../../cases/backoffice/gateway/L2.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Case = "../../" + cfg.Case
	doc, err := casecfg.LoadDocument(cfg)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := LoadTasks(cfg, doc, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Served) < 15 || len(plan.Served) > 25 || strings.TrimSpace(plan.Workflow.Info.Description) == "" {
		t.Fatalf("%d task tools, instructions %q: L2 is about twenty tools with a map", len(plan.Served), plan.Workflow.Info.Description)
	}
	served := map[string]bool{}
	for _, tool := range plan.Served {
		for _, op := range tool.Task.Ops {
			served[op.Method+" "+op.Path] = true
		}
		if tool.Task.Workflow.Agent.NeedsReview {
			t.Errorf("%s still needs review", tool.Tool.Name)
		}
	}
	for _, need := range []string{
		"GET /api/customer/searchPlayerProfile", "GET /api/customer/{customerId}", "GET /api/payment/transactions",
		"GET /api/customer/promotion/{customerId}/bonus/history", "GET /api/customer/crmlogs",
		"POST /api/payment/transaction/withdraw/reject", "POST /api/promotion/manualBonus/issue", "POST /api/customer/crmlogs",
	} {
		if !served[need] {
			t.Errorf("no task tool calls %s", need)
		}
	}
	if served["GET /api/customer/{customerId}/resetPassword"] {
		t.Error("a task tool calls the password-resetting GET")
	}
}
