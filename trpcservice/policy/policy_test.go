package policy

import (
	"context"
	"errors"
	"testing"

	"github.com/liuzengh/trpc-agent-service/trpcservice/tenant"
	trpctool "trpc.group/trpc-go/trpc-agent-go/tool"
)

func TestVisibilityExecutionPermissionAndApproval(t *testing.T) {
	approvals := NewMemoryApprovals()
	engine := &Engine{Identity: AuthenticatedIdentityAuthorizer{}, Approvals: approvals, Budgets: NewMemoryBudget()}
	request := Request{TenantID: "t", AppID: "a", UserID: "u", RequestID: "r", Policy: tenant.ToolPolicy{Allow: []string{"safe", "danger"}, Deny: []string{"blocked"}, RequireApproval: []string{"danger"}, RequestTokenBudget: 10}, EstimatedTokens: 5}
	controls, err := engine.Evaluate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	safe, danger, blocked := namedTool("safe"), namedTool("danger"), namedTool("blocked")
	if !controls.Visibility(context.Background(), safe) || controls.Visibility(context.Background(), blocked) {
		t.Fatal("visibility policy mismatch")
	}
	if !controls.Execution(context.Background(), safe) || controls.Execution(context.Background(), danger) || controls.Execution(context.Background(), blocked) {
		t.Fatal("execution allowlist mismatch")
	}
	decision, _ := controls.Permission.CheckToolPermission(context.Background(), &trpctool.PermissionRequest{ToolName: "danger"})
	if decision.Action != trpctool.PermissionActionAsk {
		t.Fatalf("decision=%+v", decision)
	}
	approvals.Grant("t", "r", "danger")
	decision, _ = controls.Permission.CheckToolPermission(context.Background(), &trpctool.PermissionRequest{ToolName: "danger"})
	if decision.Action != trpctool.PermissionActionAllow {
		t.Fatalf("approved decision=%+v", decision)
	}
	if err := engine.AuthorizeDirect(context.Background(), request, "danger"); err != nil {
		t.Fatal(err)
	}
}

func TestDangerousToolWaitsForExplicitApproval(t *testing.T) {
	approvals := NewMemoryApprovals()
	engine := &Engine{Identity: AuthenticatedIdentityAuthorizer{}, Approvals: approvals}
	request := Request{TenantID: "t", AppID: "a", UserID: "u", RequestID: "r", Policy: tenant.ToolPolicy{Allow: []string{"danger"}, RequireApproval: []string{"danger"}}}
	done := make(chan error, 1)
	go func() { done <- engine.WaitApproval(context.Background(), request, "danger") }()
	select {
	case err := <-done:
		t.Fatalf("approval did not block: %v", err)
	default:
	}
	approvals.Grant("t", "r", "danger")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestBudgetRejectsAndIsIdempotent(t *testing.T) {
	budget := NewMemoryBudget()
	request := Request{TenantID: "t", RequestID: "r", Policy: tenant.ToolPolicy{RequestTokenBudget: 2}, EstimatedTokens: 3}
	if !errors.Is(budget.Reserve(context.Background(), request), ErrBudgetExceeded) {
		t.Fatal("token budget not enforced")
	}
	request.EstimatedTokens = 1
	request.Policy.MonthlyCostBudgetCents = 1
	request.EstimatedCostMicros = 9000
	if err := budget.Reserve(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if err := budget.Reserve(context.Background(), request); err != nil {
		t.Fatal("idempotent reserve failed")
	}
	request.RequestID = "r2"
	request.EstimatedCostMicros = 2000
	if !errors.Is(budget.Reserve(context.Background(), request), ErrBudgetExceeded) {
		t.Fatal("monthly budget not enforced")
	}
}

func TestBudgetReconcileRejectsOutputOverrun(t *testing.T) {
	budget := NewMemoryBudget()
	request := Request{TenantID: "t", RequestID: "r", Policy: tenant.ToolPolicy{RequestTokenBudget: 10}, EstimatedTokens: 2}
	if err := budget.Reserve(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(budget.Reconcile(context.Background(), request, 11, 0), ErrBudgetExceeded) {
		t.Fatal("actual output overrun was accepted")
	}
}
