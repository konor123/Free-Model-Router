//go:build !windows

package config

import (
	"context"
	"errors"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// commandCredentialBackend provides best-effort adapters for native stores
// available on common Unix desktops without adding a platform dependency to
// the core package. Missing tools simply cause SecretStore to use its file
// fallback.
type commandCredentialBackend struct {
	kind string
}

func newPlatformCredentialBackend() CredentialBackend {
	switch runtime.GOOS {
	case "darwin":
		return commandCredentialBackend{kind: "security"}
	case "linux":
		return commandCredentialBackend{kind: "secret-tool"}
	default:
		return nil
	}
}

func (b commandCredentialBackend) Get(key string) (string, error) {
	var args []string
	switch b.kind {
	case "security":
		args = []string{"find-generic-password", "-s", ApplicationName, "-a", key, "-w"}
	case "secret-tool":
		args = []string{"lookup", "service", ApplicationName, "key", key}
	default:
		return "", ErrCredentialStoreUnavailable
	}
	output, err := b.run(args, "")
	if err != nil {
		return "", ErrCredentialStoreUnavailable
	}
	output = strings.TrimSpace(output)
	if output == "" {
		return "", ErrSecretNotFound
	}
	return output, nil
}

func (b commandCredentialBackend) Set(key, value string) error {
	var args []string
	switch b.kind {
	case "security":
		// security(1) accepts the password as an argument. We do not use this
		// adapter for writes because command-line arguments can be observable.
		return ErrCredentialStoreUnavailable
	case "secret-tool":
		args = []string{"store", "--label=Free-Model-Router", "service", ApplicationName, "key", key}
	default:
		return ErrCredentialStoreUnavailable
	}
	if _, err := b.run(args, value); err != nil {
		return ErrCredentialStoreUnavailable
	}
	return nil
}

func (b commandCredentialBackend) Delete(key string) error {
	var args []string
	switch b.kind {
	case "security":
		args = []string{"delete-generic-password", "-s", ApplicationName, "-a", key}
	case "secret-tool":
		args = []string{"clear", "service", ApplicationName, "key", key}
	default:
		return ErrCredentialStoreUnavailable
	}
	if _, err := b.run(args, ""); err != nil && !errors.Is(err, exec.ErrNotFound) {
		// Delete is idempotent from SecretStore's perspective. Keep the
		// backend error generic so it cannot carry secret material.
		return ErrCredentialStoreUnavailable
	}
	return nil
}

func (b commandCredentialBackend) run(args []string, input string) (string, error) {
	if b.kind == "" {
		return "", ErrCredentialStoreUnavailable
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, b.kind, args...)
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	}
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}
