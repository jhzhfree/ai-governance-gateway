// migrate operates independently of OIDC, model providers and the Web build.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jhzhfree/ai-governance-gateway/internal/database"
)

func execute(ctx context.Context, pool *pgxpool.Pool, command string, out io.Writer) error {
	switch command {
	case "up":
		if err := database.Migrate(ctx, pool); err != nil {
			return err
		}
	case "check":
		if err := database.Check(ctx, pool); err != nil {
			return err
		}
	case "status":
	default:
		return errors.New("command must be up, status or check")
	}
	report, err := database.Status(ctx, pool)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(report)
}
func run(ctx context.Context, args []string, out io.Writer) error {
	options := flag.NewFlagSet("migrate", flag.ContinueOnError)
	options.SetOutput(io.Discard)
	timeout := options.Duration("timeout", 2*time.Minute, "migration/check timeout")
	if err := options.Parse(args); err != nil {
		return errors.New("usage: migrate [--timeout=2m] up|status|check")
	}
	if options.NArg() != 1 || (*timeout <= 0) {
		return errors.New("usage: migrate [--timeout=2m] up|status|check")
	}
	command := options.Arg(0)
	if command != "up" && command != "status" && command != "check" {
		return errors.New("command must be up, status or check")
	}
	dsn := os.Getenv("AIGW_DATABASE_URL")
	if dsn == "" {
		return errors.New("AIGW_DATABASE_URL is required")
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return errors.New("database configuration failed")
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return errors.New("database connection failed")
	}
	return execute(ctx, pool, command, out)
}
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
