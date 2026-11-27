//go:build !darwin

package runner

type unsupportedSecurity struct{}

func newPlatformSecurity() SecurityRunner { return unsupportedSecurity{} }

func (unsupportedSecurity) Accounts(string) ([]string, error) {
	return nil, errKeychainUnsupported()
}

func (unsupportedSecurity) Password(string, string) (string, error) {
	return "", errKeychainUnsupported()
}
