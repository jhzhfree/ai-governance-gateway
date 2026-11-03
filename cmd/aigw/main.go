package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/jhzhfree/ai-governance-gateway/internal/terminal"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(terminal.Execute(ctx, terminal.Options{}, os.Args[1:]))
}
