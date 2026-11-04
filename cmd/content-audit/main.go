// content-audit is an operator-only maintenance command, not the remote aigw client.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jhzhfree/ai-governance-gateway/internal/contentaudit"
	"github.com/jhzhfree/ai-governance-gateway/internal/database"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "content audit maintenance failed")
		os.Exit(1)
	}
}
func run() error { return execute(os.Args[1:], os.Stdout) }
func execute(args []string, out io.Writer) error {
	if len(args) < 1 {
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
	switch args[0] {
	case "recover":
		limit := 50
		if len(args) == 2 {
			value, err := strconv.Atoi(args[1])
			if err != nil {
				return err
			}
			limit = value
		} else if len(args) != 1 {
			return contentaudit.ErrConflict
		}
		ring, err := contentaudit.ParseKeys(os.Getenv("AIGW_CONTENT_AUDIT_KEYS"), os.Getenv("AIGW_CONTENT_AUDIT_ACTIVE_KEY"))
		if err != nil {
			return err
		}
		n, err := contentaudit.New(pool, ring).Recover(ctx, limit)
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(map[string]int{"recovered": n})
	case "purge":
		limit := 1000
		if len(args) == 2 {
			n, err := strconv.Atoi(args[1])
			if err != nil {
				return err
			}
			limit = n
		} else if len(args) != 1 {
			return contentaudit.ErrConflict
		}
		n, err := service.Purge(ctx, limit)
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(map[string]int{"purged": n})
	case "verify":
		if len(args) != 2 {
			return contentaudit.ErrConflict
		}
		v, err := service.Verify(ctx, args[1])
		if err != nil {
			return err
		}
		if err := json.NewEncoder(out).Encode(v); err != nil {
			return err
		}
		if !v.Valid {
			return contentaudit.ErrUnavailable
		}
		return nil
	}
	return contentaudit.ErrConflict
}
