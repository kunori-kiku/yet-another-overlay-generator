package main

// operator.go implements `yaog-server create-operator`: the out-of-band bootstrap
// that creates an operator login account (plan-5.2). It writes an argon2id-hashed
// account into the controller FileStore at the same state dir + tenant the server
// uses; the operator then logs in at POST /login. The plaintext password is read
// without echo (interactive) or from a file / stdin (scripted) and never printed.

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/kunorikiku/yet-another-overlay-generator/internal/controller"
)

// runCreateOperator parses the create-operator flags and writes the account. It is
// dispatched from main when the first CLI arg is "create-operator". It reuses the
// controller-mode env vars (YAOG_CONTROLLER_STATE_DIR / YAOG_TENANT_ID) as flag
// defaults so the bootstrap targets the same store the server serves.
func runCreateOperator(args []string) error {
	fs := flag.NewFlagSet("create-operator", flag.ExitOnError)
	stateDir := fs.String("state-dir", os.Getenv(envControllerStateDir), "controller state dir (FileStore root); default $"+envControllerStateDir)
	tenant := fs.String("tenant", os.Getenv(envTenantID), "tenant id; default $"+envTenantID)
	username := fs.String("username", "", "operator username (required)")
	passwordFile := fs.String("password-file", "", "read the password from this file instead of prompting")
	force := fs.Bool("force", false, "overwrite an existing operator (reset its password)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *stateDir == "" {
		return fmt.Errorf("--state-dir (or $%s) is required", envControllerStateDir)
	}
	if *tenant == "" {
		return fmt.Errorf("--tenant (or $%s) is required", envTenantID)
	}
	if *username == "" {
		return errors.New("--username is required")
	}
	if err := controller.ValidateOperatorUsername(*username); err != nil {
		return err
	}

	store, err := controller.NewFileStore(*stateDir)
	if err != nil {
		return err
	}
	ctx := context.Background()
	tid := controller.TenantID(*tenant)

	// Refuse to clobber an existing account unless --force (an accidental re-create
	// would silently reset the password).
	if _, err := store.GetOperator(ctx, tid, *username); err == nil {
		if !*force {
			return fmt.Errorf("operator %q already exists (use --force to reset its password)", *username)
		}
	} else if !errors.Is(err, controller.ErrNotFound) {
		return err
	}

	password, err := readNewPassword(*passwordFile)
	if err != nil {
		return err
	}

	op, err := controller.NewOperator(*username, password, time.Now().UTC())
	if err != nil {
		return err
	}
	if err := store.PutOperator(ctx, tid, op); err != nil {
		return err
	}
	fmt.Printf("created operator %q in tenant %q\n", *username, *tenant)
	return nil
}

// readNewPassword obtains the plaintext password: from passwordFile when given; else
// without echo from an interactive terminal (prompting twice to confirm); else one
// line from stdin (scripted/piped use). The plaintext is never echoed or logged.
func readNewPassword(passwordFile string) (string, error) {
	if passwordFile != "" {
		b, err := os.ReadFile(passwordFile)
		if err != nil {
			return "", fmt.Errorf("read password file: %w", err)
		}
		return strings.TrimRight(string(b), "\r\n"), nil
	}

	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		// Non-interactive (piped): read a single line.
		sc := bufio.NewScanner(os.Stdin)
		if !sc.Scan() {
			if err := sc.Err(); err != nil {
				return "", err
			}
			return "", errors.New("no password provided on stdin")
		}
		return sc.Text(), nil
	}

	fmt.Fprint(os.Stderr, "Password: ")
	p1, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	fmt.Fprint(os.Stderr, "Confirm password: ")
	p2, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	if string(p1) != string(p2) {
		return "", errors.New("passwords do not match")
	}
	return string(p1), nil
}
