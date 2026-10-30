// Package board serves the read-only HTML board. Design §11.
package board

import (
	"errors"
	"net/http"
)

// ErrNotImplemented is returned until R4-A fills this package in.
var ErrNotImplemented = errors.New("not implemented")

// Handler is the read-only board. Every route is GET.
func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, ErrNotImplemented.Error(), http.StatusNotImplemented)
	})
}
