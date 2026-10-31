package sched

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"cardex/internal/board"
)

// Serve loops Tick until ctx is cancelled and serves board.Handler on addr.
// A host process started by Tick is not killed when ctx is cancelled.
func Serve(ctx context.Context, home, addr string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		return err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(home, "serve-addr"), []byte(ln.Addr().String()+"\n"), 0o644); err != nil {
		ln.Close()
		return err
	}
	srv := &http.Server{Handler: board.Handler()}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		shut, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(shut)
	}()
	fmt.Printf("serve listening %s\n", ln.Addr())
	runner := &Runner{Home: home}
	logPath := filepath.Join(home, "serve-loop.log")
	n := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		err := runner.Tick(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			return err
		}
		n++
		f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return err
		}
		_, werr := fmt.Fprintf(f, "iteration %d\n", n)
		cerr := f.Close()
		if werr != nil {
			return werr
		}
		if cerr != nil {
			return cerr
		}
		timer := time.NewTimer(150 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}
