// mock-provider only serves deterministic fake output on loopback.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jhzhfree/ai-governance-gateway/internal/mockprovider"
)

func main() {
	if os.Getenv("AIGW_TEST_MODE") != "1" {
		log.Fatal("mock provider is restricted to explicit tests: AIGW_TEST_MODE=1")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	server := &http.Server{Addr: "127.0.0.1:8091", Handler: http.HandlerFunc(mockprovider.Handler), ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	log.Print("mock provider on 127.0.0.1:8091; no real model calls")
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal("mock provider failed: ", err)
	}
}
