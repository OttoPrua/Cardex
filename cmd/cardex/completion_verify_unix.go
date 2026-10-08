//go:build !windows

package main

func localCompletionShell() (name string, prefix []string) {
	return "/bin/sh", []string{"-c"}
}
