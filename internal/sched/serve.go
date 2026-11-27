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

	"github.com/OttoPrua/Cardex/internal/board"
	"github.com/OttoPrua/Cardex/internal/store"
)

type serveLogKey struct{}

// WithLog attaches the writer that Serve uses for its listening and operational log lines.
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
	return serve(ctx, home, addr, time.Now, waitServeTick)
}

func serve(ctx context.Context, home, addr string, now func() time.Time, wait func(context.Context) bool) error {
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
	// The board is read-only and unauthenticated; bounded timeouts keep a slow
	// or idle client from pinning connections when it is exposed beyond loopback.
	srv := &http.Server{
		Handler:           board.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		shut, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(shut)
		board.CloseDB()
	}()
	if w := serveWriter(ctx); w != nil {
		fmt.Fprintf(w, "serve listening %s\n", ln.Addr())
	}
	runner := New(home)
	return serveTicks(ctx, runner, now, wait, os.Stderr)
}

func waitServeTick(ctx context.Context) bool {
	timer := time.NewTimer(150 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func serveTicks(ctx context.Context, runner *Runner, now func() time.Time, wait func(context.Context) bool, diagnostics io.Writer) error {
	journal := serveJournal{home: runner.Home, maxSize: serveSegmentSize, maxFiles: serveSegments}
	defer journal.close()
	var n uint64
	var failed, writeFailed, degraded bool
	var next time.Time
	w := serveWriter(ctx)
	write := func(event string) {
		record := fmt.Sprintf("%s %s tick=%d\n", now().UTC().Format(time.RFC3339Nano), event, n)
		if err := journal.append(record); err != nil {
			if !writeFailed {
				// Do not echo path names or caller-supplied strings.
				fmt.Fprintln(diagnostics, "serve-loop-v2: scheduler log write failed")
			}
			writeFailed = true
		} else {
			writeFailed = false
		}
		if w != nil {
			fmt.Fprintf(w, "serve loop %s", record)
		}
	}
	faults := make(serveFaultLimiter)
	ctx = context.WithValue(ctx, serveEventKey{}, &serveReporter{
		event: write,
		fault: func(fault serveFault) {
			// Suppression is about output, not health: an unchanged fault still
			// keeps the scheduler degraded on an otherwise quiet round.
			failed = true
			if faults.allow(now(), fault) {
				write("error step=" + fault.stage)
			}
			if !degraded {
				write("transition state=degraded")
				degraded = true
			}
		},
	})
	for ctx.Err() == nil {
		n++
		failed = false
		err := runner.Tick(ctx)
		if ctx.Err() != nil {
			break
		}
		if err != nil && !failed {
			reportServeError(ctx, "tick", err)
		}
		if !failed && degraded {
			write("transition state=healthy")
			degraded = false
		}
		current := now()
		if !current.Before(next) {
			write("heartbeat")
			next = current.Add(serveHeartbeat)
		}
		if !wait(ctx) {
			break
		}
	}
	return nil
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
