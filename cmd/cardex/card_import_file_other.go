//go:build !unix

package main

import "os"

func openImportFile(root *os.Root, name string) (*os.File, error) {
	return root.Open(name)
}
