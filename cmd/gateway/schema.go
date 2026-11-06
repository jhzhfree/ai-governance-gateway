package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jhzhfree/ai-governance-gateway/internal/database"
)

// prepareSchema runs before identity initialization, workers and the listener.
// Migrate serializes replicas and commits the complete pending batch atomically.
func prepareSchema(ctx context.Context, pool *pgxpool.Pool, c config) error {
	timeout := c.schemaTimeout
	if timeout == 0 {
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if c.schemaMode != "check" {
		log.Print("database schema upgrade started; waiting replicas remain unready")
		if err := database.Migrate(ctx, pool); err != nil {
			return fmt.Errorf("database automatic upgrade failed; service not started: %w", err)
		}
	}
	if err := database.Check(ctx, pool); err != nil {
		return fmt.Errorf("database schema is not ready; service not started: %w", err)
	}
	log.Print("database schema verified; application initialization may continue")
	return nil
}
