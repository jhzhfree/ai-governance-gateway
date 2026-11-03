package testutil

import (
	"context"
	"github.com/jhzhfree/ai-governance-gateway/internal/admission"
)

// PassAdmission is only for ledger/transport unit fixtures without control rows.
// Real server/integration fixtures use the shared PostgreSQL admission store.
type PassAdmission struct{}
type passPermit struct{}

func (passPermit) Release(context.Context) error { return nil }
func (PassAdmission) Acquire(context.Context, string, string, string) (admission.Permit, error) {
	return passPermit{}, nil
}
