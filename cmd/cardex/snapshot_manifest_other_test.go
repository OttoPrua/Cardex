//go:build !linux || !amd64

package main

func snapshotTestManifest([]string) (int, bool) { return 0, false }
