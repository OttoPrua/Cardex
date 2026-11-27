package main

import (
	"fmt"
	"io"
	"os"

	"github.com/OttoPrua/Cardex/internal/proc"
)

// Call only after the real command parser returned successfully; genuine help
// has already returned, while tokens consumed as field values are still data.
// This check is a workflow guard, not an OS sandbox or permission boundary.
func refuseManagedCardWrite(stderr io.Writer) bool {
	if os.Getenv(proc.ManagedExecutorEnv) != "1" {
		return false
	}
	fmt.Fprintln(stderr, "CardeX-managed executor cannot write cards. Use the management session; help and read-only card commands are available.")
	return true
}

// Refuse recursive scheduler/host entry while retaining ordinary help.
func refuseManagedRuntimeStart(stderr io.Writer) bool {
	if os.Getenv(proc.ManagedExecutorEnv) != "1" {
		return false
	}
	fmt.Fprintln(stderr, "CardeX-managed executor cannot start a CardeX scheduler or host. Use the management session.")
	return true
}
