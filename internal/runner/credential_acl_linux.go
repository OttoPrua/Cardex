package runner

import "os"

// The mode group bits are the maximum effective permissions for named POSIX
// ACL users/groups on Linux. Requiring all of those bits off covers them.
func verifyCredentialACL(string, os.FileInfo) error { return nil }
