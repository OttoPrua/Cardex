//go:build darwin

package runner

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"time"
)

// darwinSecurity reads the user keychain through /usr/bin/security.
// It does not use sudo. Command output is never copied into errors: a failed
// security invocation can print the password on stderr.
type darwinSecurity struct{}

func newPlatformSecurity() SecurityRunner { return darwinSecurity{} }

func (darwinSecurity) Accounts(service string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/security", "dump-keychain")
	cmd.Env = []string{"LC_ALL=C"}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, keychainCommandError(stderr.String())
	}
	return parseKeychainAccounts(stdout.String(), service), nil
}

func (darwinSecurity) Password(service, account string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/security", keychainPasswordArgs(service, account)...)
	cmd.Env = []string{"LC_ALL=C"}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", keychainCommandError(stderr.String())
	}
	value := strings.TrimRight(stdout.String(), "\r\n")
	if value == "" {
		return "", errKeychainNotFound
	}
	return value, nil
}
