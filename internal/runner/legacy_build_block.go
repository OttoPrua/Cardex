package runner

import (
	"errors"
	"os"
	"strings"

	"github.com/OttoPrua/Cardex/internal/proc"
)

type legacyClaudeBlock uint8

const (
	legacyProfileInvalid legacyClaudeBlock = iota + 1
	legacyCredentialProtected
	legacyAuthEnvEmpty
	legacyAuthMissing
)

// A local configuration block is not an executor start or a transport result.
// In particular, a transient auth file IO error remains an ordinary Build
// error and must not be interpreted as a persisted configuration block.
type legacyClaudeBuildError struct {
	kind legacyClaudeBlock
	err  error
}

func (e legacyClaudeBuildError) Error() string { return e.err.Error() }
func (e legacyClaudeBuildError) Unwrap() error { return e.err }

func IsLegacyClaudeBuildBlock(err error) bool {
	var blocked legacyClaudeBuildError
	return errors.As(err, &blocked)
}

// LegacyClaudeBuildBlockCurrent repeats only the local predicate for a block
// already returned by Build. The caller separately checks the exact profile
// configuration. It does not build a command, open an auth file, or call a
// provider.
func LegacyClaudeBuildBlockCurrent(err error, raw []byte, dropEnv []string) bool {
	var blocked legacyClaudeBuildError
	if !errors.As(err, &blocked) || IsCompatibleEngine(raw) {
		return false
	}
	p, ok := ParseEngineProfile(raw)
	switch blocked.kind {
	case legacyProfileInvalid:
		return !ok
	case legacyCredentialProtected:
		return ok && proc.CompatibleCredentialName(p.AuthEnv, dropEnv)
	case legacyAuthEnvEmpty:
		return ok && p.AuthEnv != "" && !proc.CompatibleCredentialName(p.AuthEnv, dropEnv) &&
			strings.TrimSpace(os.Getenv(p.AuthEnv)) == ""
	case legacyAuthMissing:
		return ok && p.AuthEnv == "" && p.AuthFile == "" && strings.TrimSpace(p.AuthValue) == ""
	default:
		return false
	}
}
