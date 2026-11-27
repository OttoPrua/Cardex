package main

import (
	"path/filepath"

	"github.com/OttoPrua/Cardex/internal/store"
)

func openStoreForWrite(home string) (*store.Store, error) {
	dir, err := resolveHome(home)
	if err != nil {
		return nil, err
	}
	return store.OpenForWrite(filepath.Join(dir, "cardex.db"))
}
