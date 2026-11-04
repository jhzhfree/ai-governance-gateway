// local-admin initializes/recovers an offline administrator using operator DB credentials.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jhzhfree/ai-governance-gateway/internal/database"
	"github.com/jhzhfree/ai-governance-gateway/internal/localauth"
	"golang.org/x/term"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "local administrator operation failed; check schema, mode and password requirements")
		os.Exit(1)
	}
}
func run() error {
	options := flag.NewFlagSet("local-admin", flag.ContinueOnError)
	options.SetOutput(io.Discard)
	origin := options.String("origin", os.Getenv("AIGW_BROWSER_ORIGIN"), "exact gateway origin")
	username := options.String("username", "", "administrator username")
	stdin := options.Bool("password-stdin", false, "read one password line from protected stdin")
	allow := options.Bool("allow-loopback-http", false, "explicit loopback development origin")
	if err := options.Parse(os.Args[1:]); err != nil || options.NArg() != 1 {
		return errors.New("usage")
	}
	action := options.Arg(0)
	if action != "init" && action != "recover" {
		return errors.New("usage")
	}
	var password []byte
	var err error
	if *stdin {
		line, e := bufio.NewReader(io.LimitReader(os.Stdin, 258)).ReadString('\n')
		if e != nil && e != io.EOF {
			return e
		}
		password = []byte(strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"))
	} else {
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return errors.New("use --password-stdin")
		}
		fmt.Fprint(os.Stderr, "New administrator password (12+ characters): ")
		password, err = term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return err
		}
	}
	defer func() {
		for i := range password {
			password[i] = 0
		}
	}()
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
	service, err := localauth.New(pool, *origin, *allow)
	if err != nil {
		return err
	}
	var user localauth.User
	if action == "init" {
		user, err = service.Initialize(ctx, *username, string(password))
	} else {
		user, err = service.Recover(ctx, *username, string(password))
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"issuer": service.Issuer(), "subject": user.ID, "username": user.Username, "must_change_password": true, "action": action})
}
