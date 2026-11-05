package budget_test

import (
	"context"
	"github.com/jhzhfree/ai-governance-gateway/internal/testutil"
	"testing"
)

func TestDepartmentCaptureFailureRollsBackReservationAndBudget(t *testing.T) {
	pool, s, scope := testutil.Database(t)
	ctx := context.Background()
	testutil.Accounts(t, s, scope, 100, 100)
	if _, err := pool.Exec(ctx, `ALTER TABLE request_department_attributions ADD CONSTRAINT synthetic_capture_failure CHECK(request_id<>'capture-fails')`); err != nil {
		t.Fatal(err)
	}
	if created, err := s.Reserve(ctx, quote(scope, "capture-fails", 10)); err == nil || created {
		t.Fatal("capture failure admitted request")
	}
	balances(t, pool, scope, 0, 0)
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM budget_reservations WHERE request_id='capture-fails'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("failed capture leaked reservation")
	}
	if created, err := s.Reserve(ctx, quote(scope, "capture-ok", 10)); err != nil || !created {
		t.Fatal(err)
	}
	if created, err := s.Reserve(ctx, quote(scope, "capture-ok", 10)); err != nil || created {
		t.Fatal("replay changed admission")
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM request_department_attributions WHERE request_id='capture-ok' AND department_path='[]'::jsonb AND configuration_revision=0`).Scan(&count); err != nil || count != 1 {
		t.Fatal("unknown/unassigned or replay attribution wrong")
	}
}
