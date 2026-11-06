package budget

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jhzhfree/ai-governance-gateway/internal/deviceproof"
)

func captureDevice(ctx context.Context, tx pgx.Tx, q Quote) error {
	b, ok := deviceproof.FromContext(ctx)
	if !ok {
		return nil
	}
	if b.Organization != q.Organization || b.Project != q.Project {
		return errors.New("device admission scope mismatch")
	}
	_, err := tx.Exec(ctx, `INSERT INTO device_request_bindings(request_id,device_id,session_id) VALUES($1,$2,$3)`, q.RequestID, b.DeviceID, b.SessionID)
	return err
}
