package httpapi_test

import (
	"context"
	"errors"
	"github.com/jhzhfree/ai-governance-gateway/internal/budget"
	"github.com/jhzhfree/ai-governance-gateway/internal/control"
	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"strings"
	"sync"
	"testing"
	"time"
)

func approvalPolicyBody(rev int64, enabled bool) any {
	return map[string]any{"expected_revision": rev, "enabled": enabled, "reason": "synthetic two-person policy"}
}
func approvalRequestBody(period string, rev, limit int64) any {
	return map[string]any{"period": period, "expected_revision": rev, "limit": limit, "reason": "synthetic budget request"}
}
func TestBudgetApprovalIndependentDecisionAndNoDirectBypass(t *testing.T) {
	f := setup(t)
	o := f.org("alice")
	p := f.project("alice", o)
	f.member(o, "bob", "admin")
	base := projectPath(o, p) + "/budget"
	policy := orgPath(o) + "/budget/approval-policy"
	initial := parse[control.BudgetApprovalPolicy](t, f.request(f.token("alice"), "GET", policy, nil, 200))
	if initial.Enabled || initial.Revision != 0 {
		t.Fatal("approval unexpectedly enabled")
	}
	f.request(f.token("bob"), "PUT", policy, approvalPolicyBody(0, true), 403)
	f.request(f.token("alice"), "PUT", policy, approvalPolicyBody(0, true), 200)
	denied := f.request(f.token("alice"), "PUT", base, approvalRequestBody(o.Period, 0, 100), 409)
	if !bytesContain(denied, "budget_approval_required") {
		t.Fatal("direct mutation not approval gated")
	}
	f.request(f.token("alice"), "POST", orgPath(o)+"/projects", map[string]any{"name": "bypass initial", "limit_amount": 10}, 409)
	f.request(f.token("alice"), "POST", orgPath(o)+"/projects", map[string]any{"name": "zero initial", "limit_amount": 0}, 201)
	before := parse[control.BudgetAccount](t, f.request(f.token("alice"), "GET", base, nil, 200))
	proposal := parse[control.BudgetApproval](t, f.request(f.token("alice"), "POST", base+"/approvals", approvalRequestBody(o.Period, before.Revision, before.Limit+100), 201))
	f.request(f.token("alice"), "POST", base+"/approvals", approvalRequestBody(o.Period, before.Revision, before.Limit+200), 409)
	if now := parse[control.BudgetAccount](t, f.request(f.token("bob"), "GET", base, nil, 200)); now != before {
		t.Fatal("proposal changed financial account")
	}
	decision := map[string]string{"reason": "independent review"}
	f.request(f.token("alice"), "POST", base+"/approvals/"+proposal.ID+"/approve", decision, 403)
	f.request(f.token("outsider"), "GET", base+"/approvals", nil, 404)
	approved := parse[control.BudgetApproval](t, f.request(f.token("bob"), "POST", base+"/approvals/"+proposal.ID+"/approve", decision, 200))
	if approved.State != "approved" || approved.Decider == nil || approved.Decider.Subject != "bob" || approved.AppliedRevision == nil || *approved.AppliedRevision != 1 {
		t.Fatal("decision receipt incomplete")
	}
	after := parse[control.BudgetAccount](t, f.request(f.token("alice"), "GET", base, nil, 200))
	if after.Limit != before.Limit+100 || after.Revision != 1 || after.Spent != before.Spent || after.Held != before.Held {
		t.Fatal("approval modified usage or omitted limit")
	}
	f.request(f.token("bob"), "POST", base+"/approvals/"+proposal.ID+"/approve", decision, 409)
	other := f.org("alice")
	otherP := f.project("alice", other)
	f.request(f.token("alice"), "POST", projectPath(other, otherP)+"/budget/approvals/"+proposal.ID+"/approve", decision, 404)
	history := parse[struct {
		Data []control.BudgetApprovalPolicyChange `json:"data"`
	}](t, f.request(f.token("alice"), "GET", policy+"/changes", nil, 200))
	if len(history.Data) != 1 || !history.Data[0].Enabled || history.Data[0].Reason == "" {
		t.Fatal("policy reason history missing")
	}
}
func bytesContain(data []byte, s string) bool { return strings.Contains(string(data), s) }
func TestBudgetApprovalRechecksBalancePeriodExpiryAndRequester(t *testing.T) {
	f := setup(t)
	o := f.org("alice")
	p := f.project("alice", o)
	f.member(o, "bob", "admin")
	base := projectPath(o, p) + "/budget"
	f.request(f.token("alice"), "PUT", orgPath(o)+"/budget/approval-policy", approvalPolicyBody(0, true), 200)
	propose := func(subject string, limit int64) control.BudgetApproval {
		return parse[control.BudgetApproval](t, f.request(f.token(subject), "POST", base+"/approvals", approvalRequestBody(o.Period, 0, limit), 201))
	}
	decision := map[string]string{"reason": "review"}
	proposal := propose("alice", 20)
	ledger := budget.New(f.pool)
	if _, err := ledger.Reserve(context.Background(), budget.Quote{Scope: budget.Scope{Organization: o.ID, Project: p.ID, Period: o.Period, Currency: o.Currency}, RequestID: "approval-held", PriceVersion: "synthetic", Amount: 30}); err != nil {
		t.Fatal(err)
	}
	f.request(f.token("bob"), "POST", base+"/approvals/"+proposal.ID+"/approve", decision, 409)
	f.request(f.token("alice"), "POST", base+"/approvals/"+proposal.ID+"/cancel", decision, 200)
	proposal = propose("bob", 100)
	f.member(o, "bob", "developer")
	f.request(f.token("alice"), "POST", base+"/approvals/"+proposal.ID+"/approve", decision, 403)
	f.member(o, "bob", "admin")
	f.request(f.token("alice"), "PUT", orgPath(o)+"/members/enabled", lifecycleBody(f, "bob", 0, false, ""), 200)
	f.request(f.token("alice"), "POST", base+"/approvals/"+proposal.ID+"/approve", decision, 404)
	f.request(f.token("alice"), "POST", base+"/approvals/"+proposal.ID+"/reject", decision, 200)
	f.request(f.token("alice"), "PUT", orgPath(o)+"/members/enabled", lifecycleBody(f, "bob", 1, true, "admin"), 200)
	proposal = propose("alice", 200)
	if _, err := f.pool.Exec(context.Background(), `UPDATE budget_approval_requests SET created_at=clock_timestamp()-interval '25 hours',expires_at=clock_timestamp()-interval '1 hour' WHERE id=$1`, proposal.ID); err != nil {
		t.Fatal(err)
	}
	f.request(f.token("bob"), "POST", base+"/approvals/"+proposal.ID+"/approve", decision, 409)
	proposal = propose("alice", 200)
	f.request(f.token("alice"), "POST", orgPath(o)+"/budget/period", map[string]string{"expected_period": o.Period, "next_period": "2026-12"}, 204)
	f.request(f.token("bob"), "POST", base+"/approvals/"+proposal.ID+"/approve", decision, 409)
	f.request(f.token("alice"), "POST", base+"/approvals", approvalRequestBody("2026-12", 0, 200), 201)
}
func TestBudgetApprovalAuditFailureRollsBackDecisionAndPolicy(t *testing.T) {
	f := setup(t)
	o := f.org("alice")
	p := f.project("alice", o)
	f.member(o, "bob", "admin")
	base := projectPath(o, p) + "/budget"
	policy := orgPath(o) + "/budget/approval-policy"
	f.request(f.token("alice"), "PUT", policy, approvalPolicyBody(0, true), 200)
	before := parse[control.BudgetAccount](t, f.request(f.token("alice"), "GET", base, nil, 200))
	proposal := parse[control.BudgetApproval](t, f.request(f.token("alice"), "POST", base+"/approvals", approvalRequestBody(o.Period, 0, before.Limit+100), 201))
	if _, err := f.pool.Exec(context.Background(), `ALTER TABLE audit_events ADD CONSTRAINT reject_approval CHECK(action<>'budget.approval_approved') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	f.request(f.token("bob"), "POST", base+"/approvals/"+proposal.ID+"/approve", map[string]string{"reason": "rollback"}, 503)
	if now := parse[control.BudgetAccount](t, f.request(f.token("alice"), "GET", base, nil, 200)); now != before {
		t.Fatal("audit failure committed budget")
	}
	var state string
	if err := f.pool.QueryRow(context.Background(), `SELECT state FROM budget_approval_requests WHERE id=$1`, proposal.ID).Scan(&state); err != nil || state != "pending" {
		t.Fatal("failed decision changed request")
	}
	if _, err := f.pool.Exec(context.Background(), `ALTER TABLE audit_events ADD CONSTRAINT reject_policy CHECK(action<>'budget.approval_policy_changed') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	f.request(f.token("alice"), "PUT", policy, approvalPolicyBody(1, false), 503)
	current := parse[control.BudgetApprovalPolicy](t, f.request(f.token("alice"), "GET", policy, nil, 200))
	if !current.Enabled || current.Revision != 1 {
		t.Fatal("failed policy update bypassed guard")
	}
	if _, err := f.pool.Exec(context.Background(), `ALTER TABLE audit_events DROP CONSTRAINT reject_policy`); err != nil {
		t.Fatal(err)
	}
	f.request(f.token("alice"), "PUT", policy, approvalPolicyBody(1, false), 200)
	var reason string
	if err := f.pool.QueryRow(context.Background(), `SELECT state,decision_reason FROM budget_approval_requests WHERE id=$1`, proposal.ID).Scan(&state, &reason); err != nil || state != "canceled" || reason == "" {
		t.Fatal("policy change left stale request pending")
	}
	f.request(f.token("alice"), "PUT", base, approvalRequestBody(o.Period, 0, before.Limit+1), 200)
}
func TestBudgetApprovalConcurrentDecisionsApplyOnce(t *testing.T) {
	f := setup(t)
	o := f.org("alice")
	p := f.project("alice", o)
	f.member(o, "bob", "admin")
	f.member(o, "carol", "admin")
	base := projectPath(o, p) + "/budget"
	f.request(f.token("alice"), "PUT", orgPath(o)+"/budget/approval-policy", approvalPolicyBody(0, true), 200)
	before := parse[control.BudgetAccount](t, f.request(f.token("alice"), "GET", base, nil, 200))
	proposal := parse[control.BudgetApproval](t, f.request(f.token("alice"), "POST", base+"/approvals", approvalRequestBody(o.Period, 0, before.Limit+10), 201))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, subject := range []string{"bob", "carol"} {
		wg.Add(1)
		go func(subject string) {
			defer wg.Done()
			_, err := f.store.DecideBudgetApproval(ctx, identity.Principal{Issuer: f.issuer.URL, Subject: subject}, o.ID, p.ID, proposal.ID, "approve", "parallel review")
			results <- err
		}(subject)
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, control.ErrBudgetConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal("concurrent decision not fenced")
	}
	after := parse[control.BudgetAccount](t, f.request(f.token("alice"), "GET", base, nil, 200))
	if after.Revision != 1 || after.Limit != before.Limit+10 {
		t.Fatal("duplicate budget application")
	}
}
