package sched

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"cardex/internal/board"
	"cardex/internal/store"
)

type serveLogKey struct{}

// WithLog attaches the writer that Serve uses for its listening and iteration lines.
func WithLog(ctx context.Context, w io.Writer) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if w == nil {
		return ctx
	}
	return context.WithValue(ctx, serveLogKey{}, w)
}

func serveWriter(ctx context.Context) io.Writer {
	if ctx == nil {
		return nil
	}
	w, _ := ctx.Value(serveLogKey{}).(io.Writer)
	return w
}

// Serve loops Tick until ctx is cancelled and serves board.Handler on addr.
// A host process started by Tick is not killed when ctx is cancelled.
func Serve(ctx context.Context, home, addr string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		return err
	}
	if err := serveStartup(home); err != nil {
		fmt.Fprintf(os.Stderr, "serve: %s\n", err)
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
	if w := serveWriter(ctx); w != nil {
		fmt.Fprintf(w, "serve listening %s\n", ln.Addr())
	}
	runner := New(home)
	logPath := filepath.Join(home, "serve-loop.log")
	n := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		_ = runner.Tick(ctx)
		if ctx.Err() != nil {
			return nil
		}
		n++
		if w := serveWriter(ctx); w != nil {
			fmt.Fprintf(w, "serve loop iteration %d\n", n)
		}
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

// serveStartup opens the database and, when config.json exists, checks that
// it parses. A missing config file keeps the current default-schedule path.
func serveStartup(home string) error {
	db, err := store.Open(filepath.Join(home, "cardex.db"))
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	db.Close()
	path := filepath.Join(home, "config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("config: %w", err)
	}
	if !json.Valid(data) {
		return fmt.Errorf("config: cannot parse %s", path)
	}
	return nil
}
