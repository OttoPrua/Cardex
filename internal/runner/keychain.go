package runner

import (
	"errors"
	"runtime"
	"sort"
	"strings"
)

var (
	errKeychainNotFound   = errors.New("keychain item not found")
	errKeychainAmbiguous  = errors.New("keychain service matches more than one item; set credential.account")
	errKeychainPermission = errors.New("keychain permission denied")
)

func errKeychainUnsupported() error {
	return errors.New("keychain credentials are not supported on " + runtime.GOOS)
}

// SecurityRunner lists and reads generic passwords in the user keychain.
// Accounts must not return secret values. Password returns the secret only to
// the caller, which must not place it in argv, logs, or errors.
type SecurityRunner interface {
	Accounts(service string) ([]string, error)
	Password(service, account string) (string, error)
}

// securityRunner is the process-wide keychain. Tests replace it; production
// uses the user-domain system command on darwin and refuses elsewhere.
var securityRunner SecurityRunner = newPlatformSecurity()

// SetSecurityRunnerForTest installs a keychain double and returns the restore func.
func SetSecurityRunnerForTest(s SecurityRunner) func() {
	prev := securityRunner
	securityRunner = s
	return func() { securityRunner = prev }
}

func keychainCommandError(stderr string) error {
	low := strings.ToLower(stderr)
	switch {
	case strings.Contains(low, "user interaction is not allowed"),
		strings.Contains(low, "interaction not allowed"),
		strings.Contains(low, "errsecauth"),
		strings.Contains(low, "authorization"),
		strings.Contains(low, "permission"):
		return errKeychainPermission
	default:
		return errKeychainNotFound
	}
}

// parseKeychainAccounts reads account names for service from a security
// dump-keychain transcript. It ignores secret data lines.
func parseKeychainAccounts(dump, service string) []string {
	var attrs map[string]string
	var out []string
	add := func() {
		if attrs["svce"] != service {
			return
		}
		// Invalid or unprintable account text must count as an ambiguous
		// match, never disappear and turn a multi-match into a single item.
		out = append(out, attrs["acct"])
	}
	for _, line := range strings.Split(dump, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "class:") {
			add()
			attrs = nil
			if line == `class: "genp"` {
				attrs = map[string]string{}
			}
			continue
		}
		if attrs == nil {
			continue
		}
		for _, field := range []string{"svce", "acct"} {
			prefix := `"` + field + `"<blob>=`
			if raw, ok := strings.CutPrefix(line, prefix); ok && len(raw) >= 2 &&
				strings.HasPrefix(raw, `"`) && strings.HasSuffix(raw, `"`) {
				attrs[field] = raw[1 : len(raw)-1]
			}
		}
	}
	add()
	sort.Strings(out)
	return out
}

// keychainPasswordArgs is the user-domain security invocation. The secret is
// read from stdout, never passed in argv.
func keychainPasswordArgs(service, account string) []string {
	return []string{"find-generic-password", "-s", service, "-a", account, "-w"}
}
