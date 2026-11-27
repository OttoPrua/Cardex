//go:build unix

package main

import (
	"os"

	"golang.org/x/sys/unix"
)

func openImportFile(root *os.Root, name string) (*os.File, error) {
	// A FIFO swapped in after the initial stat must not block the CLI.
	return root.OpenFile(name, os.O_RDONLY|unix.O_NONBLOCK, 0)
}
