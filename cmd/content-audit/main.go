// content-audit is an operator-only maintenance command, not the remote aigw client.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jhzhfree/ai-governance-gateway/internal/contentaudit"
	"github.com/jhzhfree/ai-governance-gateway/internal/database"
	"os"
	"strconv"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "content audit maintenance failed")
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) < 2 {
		return contentaudit.ErrConflict
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, os.Getenv("AIGW_DATABASE_URL"))
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := database.Check(ctx, pool); err != nil {
		return err
	}
	service := contentaudit.New(pool, nil)
	switch os.Args[1] {
	case "purge":
		limit := 1000
		if len(os.Args) == 3 {
			n, err := strconv.Atoi(os.Args[2])
			if err != nil {
				return err
			}
			limit = n
		} else if len(os.Args) != 2 {
			return contentaudit.ErrConflict
		}
		n, err := service.Purge(ctx, limit)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]int{"purged": n})
	case "verify":
		if len(os.Args) != 3 {
			return contentaudit.ErrConflict
		}
		v, err := service.Verify(ctx, os.Args[2])
		if err != nil {
			return err
		}
		if err := json.NewEncoder(os.Stdout).Encode(v); err != nil {
			return err
		}
		if !v.Valid {
			return contentaudit.ErrUnavailable
		}
		return nil
	}
	return contentaudit.ErrConflict
}
