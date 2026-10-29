package store

import (
	"crypto/rand"
	"encoding/hex"
)

func newID(prefix string) (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(b[:]), nil
}

func ensureID(id, prefix string) (string, error) {
	if id != "" {
		return id, nil
	}
	return newID(prefix)
}
