package host_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/host"
	"github.com/OttoPrua/Cardex/internal/runner"
	"github.com/OttoPrua/Cardex/internal/store"
	"github.com/OttoPrua/Cardex/internal/workspace"
)

// observingManager records the context Commit actually receives.
// A cancelled context fails the commit, which is the bug under test.
type observingManager struct {
	mu        sync.Mutex
	calls     int
	cancelled bool
	deadline  time.Time
	hasDL     bool
	hash      string
}

func (m *observingManager) Commit(ctx context.Context, _, _ string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	m.cancelled = ctx.Err() != nil
	m.deadline, m.hasDL = ctx.Deadline()
	if ctx.Err() != nil {
		return "", errors.New("commit on cancelled context")
	}
	return m.hash, nil
}

func (m *observingManager) Create(context.Context, workspace.CreateRequest) (workspace.Worktree, error) {
	return workspace.Worktree{}, errors.New("not used")
}

func (m *observingManager) Diff(context.Context, string, string) (workspace.Diff, error) {
	return workspace.Diff{}, errors.New("not used")
}

func (m *observingManager) Remove(context.Context, string) error { return errors.New("not used") }

func (m *observingManager) BlockPush(context.Context, string) error { return errors.New("not used") }

func (m *observingManager) snapshot() (calls int, cancelled, hasDL bool, dl time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls, m.cancelled, m.hasDL, m.deadline
}

func TestAttemptLogFlushedWhileRunning(t *testing.T) {
	const (
		prefix = "PREFIX-r2a-f1"
		tail   = "TAIL-r2a-f1"
	)
	payload := "tokens=42\n" + `{"type":"error","error":{"message":"invalid api key"}}` + "\n" + tail + "\n"
	for _, tc := range []struct {
		name string
		pty  bool
	}{
		{name: "pipe"},
		{name: "pty", pty: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, id, _ := seedAttempt(t, 1, func(string) any {
				doc := map[string]any{
					"attempts": map[string]any{
						"0": map[string]any{
							"output":     prefix + "\n",
							"tail_after": "3s",
							"tail":       payload,
						},
					},
				}
				if tc.pty {
					doc["needs_pty"] = true
				}
				return doc
			})
			logPath := filepath.Join(home, "attempts", id, "output.log")
			errc := make(chan error, 1)
			done := make(chan struct{})
			go func() {
				errc <- host.Run(context.Background(), home, id)
				close(done)
			}()
			defer func() {
				select {
				case <-done:
				case <-time.After(8 * time.Second):
				}
			}()

			deadline := time.Now().Add(5 * time.Second)
			var partial []byte
			for {
				partial, _ = os.ReadFile(logPath)
				if strings.Contains(string(partial), prefix) {
					break
				}
				select {
				case err := <-errc:
					t.Fatalf("Run returned before %s was flushed: %v\nlog %q", prefix, err, partial)
				default:
				}
				if time.Now().After(deadline) {
					t.Fatalf("output.log missing %s while the attempt ran: %q", prefix, partial)
				}
				time.Sleep(10 * time.Millisecond)
			}
			select {
			case err := <-errc:
				t.Fatalf("Run returned before the tail wait: %v", err)
			default:
			}
			if strings.Contains(string(partial), tail) {
				t.Fatalf("tail was already in the log before the attempt finished: %q", partial)
			}

			var runErr error
			select {
			case runErr = <-errc:
			case <-time.After(8 * time.Second):
				t.Fatal("Run did not finish")
			}
			if runErr != nil {
				t.Fatal(runErr)
			}
			logb, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			log := string(logb)
			if !strings.Contains(log, prefix) || !strings.Contains(log, tail) || !strings.Contains(log, "tokens=42") {
				t.Fatalf("full log %q", log)
			}
			attempt, events := loadAttempt(t, home, id)
			if attempt.Exit == nil {
				t.Fatal("missing exit")
			}
			wantUsage := runner.Fake{}.ParseUsage(logb)
			if attempt.Usage != wantUsage || wantUsage.Tokens == 0 {
				t.Fatalf("usage %+v want %+v", attempt.Usage, wantUsage)
			}
			wantClass := runner.Fake{}.Classify(*attempt.Exit, logb)
			if wantClass == "" || attempt.ErrorClass != wantClass {
				t.Fatalf("class %q want %q", attempt.ErrorClass, wantClass)
			}
			if _, ok := eventType(events, "host_finish"); !ok {
				t.Fatal("missing host_finish")
			}
		})
	}
}

func TestCancelDoesNotDropExecutionResult(t *testing.T) {
	for _, tc := range []struct {
		name      string
		failStart bool
	}{
		{name: "cancel-during-finish"},
		{name: "fail-start", failStart: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.failStart {
				testFailStartPersists(t)
				return
			}
			testCancelDuringFinish(t)
		})
	}
}

func testCancelDuringFinish(t *testing.T) {
	t.Helper()
	const prefix = "LIVE-PREFIX-r2a-f1"
	home, id, _ := seedAttempt(t, 1, func(string) any {
		return map[string]any{
			"attempts": map[string]any{
				"0": map[string]any{
					"output": prefix + "\ntokens=17\n",
					"hold":   "20s",
				},
			},
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := &observingManager{hash: "f1-head"}
	logPath := filepath.Join(home, "attempts", id, "output.log")
	errc := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		errc <- host.Run(host.WithWorkspace(ctx, mgr), home, id)
		close(done)
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(8 * time.Second):
		}
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		partial, _ := os.ReadFile(logPath)
		if strings.Contains(string(partial), prefix) && strings.Contains(string(partial), "tokens=17") {
			break
		}
		select {
		case err := <-errc:
			t.Fatalf("Run returned before cancel: %v\nlog %q", err, partial)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("executor output was not flushed before cancel: %q", partial)
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case err := <-errc:
		t.Fatalf("Run returned before cancel: %v", err)
	default:
	}
	cancel()

	var runErr error
	select {
	case runErr = <-errc:
	case <-time.After(8 * time.Second):
		t.Fatal("Run did not finish after cancel")
	}
	if runErr != nil {
		t.Fatal(runErr)
	}
	calls, cancelled, hasDL, dl := mgr.snapshot()
	if calls != 1 || cancelled || !hasDL || !dl.After(time.Now().Add(-time.Second)) || !dl.Before(time.Now().Add(2*time.Minute)) {
		t.Fatalf("commit calls=%d cancelled=%v deadline=%v has=%v", calls, cancelled, dl, hasDL)
	}
	logb, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	attempt, events := loadAttempt(t, home, id)
	if attempt.Exit == nil || attempt.EndedAt.IsZero() {
		t.Fatalf("exit %#v ended %v", attempt.Exit, attempt.EndedAt)
	}
	want := runner.Fake{}.ParseUsage(logb)
	if attempt.Usage != want || want.Tokens == 0 {
		t.Fatalf("usage %+v want %+v log %q", attempt.Usage, want, logb)
	}
	if attempt.HeadCommit != mgr.hash {
		t.Fatalf("head %q", attempt.HeadCommit)
	}
	if _, ok := eventType(events, "host_finish"); !ok {
		t.Fatal("missing host_finish")
	}
}

func testFailStartPersists(t *testing.T) {
	t.Helper()
	home, id, _ := seedAttempt(t, 1, func(string) any {
		return map[string]any{
			"attempts": map[string]any{
				"0": map[string]any{"output": "should-not-run\n", "hold": "20s"},
			},
		}
	})
	prompt := filepath.Join(home, "prompt.md")
	if err := os.Remove(prompt); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(prompt, 0o644); err != nil {
		t.Fatal(err)
	}
	defer func() {
		f, err := os.OpenFile(prompt, os.O_WRONLY|syscall.O_NONBLOCK, 0)
		if err == nil {
			_ = f.Close()
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := &observingManager{hash: "f1-head"}
	errc := make(chan error, 1)
	go func() {
		errc <- host.Run(host.WithWorkspace(ctx, mgr), home, id)
	}()

	db, err := store.Open(filepath.Join(home, "cardex.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		attempt, rerr := db.GetAttempt(context.Background(), id)
		if rerr == nil && attempt.PID != 0 && attempt.Exit == nil {
			break
		}
		select {
		case err := <-errc:
			t.Fatalf("Run returned before the prompt open blocked: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("attempt did not record a start before prompt open")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()

	var runErr error
	select {
	case runErr = <-errc:
	case <-time.After(5 * time.Second):
		t.Fatal("failStart did not return after cancel")
	}
	if !errors.Is(runErr, context.Canceled) {
		t.Fatalf("Run err %v", runErr)
	}
	calls, _, _, _ := mgr.snapshot()
	if calls != 0 {
		t.Fatalf("Commit calls %d on failed start", calls)
	}
	attempt, events := loadAttempt(t, home, id)
	if attempt.Exit == nil || *attempt.Exit != 127 || attempt.EndedAt.IsZero() {
		t.Fatalf("exit %#v ended %v", attempt.Exit, attempt.EndedAt)
	}
	if _, ok := eventType(events, "host_finish"); !ok {
		t.Fatal("missing host_finish")
	}
}

// blockingUntilDoneManager blocks in Commit until the context it receives is
// done, then returns that error. Returning immediately is not coverage.
type blockingUntilDoneManager struct {
	mu               sync.Mutex
	calls            int
	cancelledAtEntry bool
	hasDL            bool
	remain           time.Duration
	err              error
	waited           time.Duration
	doneAt           time.Time
	entered          chan struct{}
}

func newBlockingUntilDoneManager() *blockingUntilDoneManager {
	return &blockingUntilDoneManager{entered: make(chan struct{})}
}

func (m *blockingUntilDoneManager) Commit(ctx context.Context, _, _ string) (string, error) {
	start := time.Now()
	dl, hasDL := ctx.Deadline()
	var remain time.Duration
	if hasDL {
		remain = time.Until(dl)
	}
	m.mu.Lock()
	m.calls++
	m.cancelledAtEntry = ctx.Err() != nil
	m.hasDL = hasDL
	m.remain = remain
	m.mu.Unlock()
	close(m.entered)

	<-ctx.Done()
	doneAt := time.Now()
	err := ctx.Err()
	m.mu.Lock()
	m.err = err
	m.waited = time.Since(start)
	m.doneAt = doneAt
	m.mu.Unlock()
	return "", err
}

func (m *blockingUntilDoneManager) Create(context.Context, workspace.CreateRequest) (workspace.Worktree, error) {
	return workspace.Worktree{}, errors.New("not used")
}

func (m *blockingUntilDoneManager) Diff(context.Context, string, string) (workspace.Diff, error) {
	return workspace.Diff{}, errors.New("not used")
}

func (m *blockingUntilDoneManager) Remove(context.Context, string) error {
	return errors.New("not used")
}

func (m *blockingUntilDoneManager) BlockPush(context.Context, string) error {
	return errors.New("not used")
}

func (m *blockingUntilDoneManager) snapshot() (calls int, cancelledAtEntry, hasDL bool, remain time.Duration, err error, waited time.Duration, doneAt time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls, m.cancelledAtEntry, m.hasDL, m.remain, m.err, m.waited, m.doneAt
}

func TestCommitDeadlineDoesNotDropExecutionResult(t *testing.T) {
	const bound = time.Second
	restore := host.SetResultPersistTimeoutForTest(bound)
	t.Cleanup(restore)

	for _, tc := range []struct {
		name   string
		pty    bool
		cancel bool
	}{
		{name: "pipe"},
		{name: "pty", pty: true},
		{name: "cancel-while-commit-blocks", cancel: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testCommitDeadlineDoesNotDrop(t, bound, tc.pty, tc.cancel)
		})
	}
}

func testCommitDeadlineDoesNotDrop(t *testing.T, bound time.Duration, pty, cancelRun bool) {
	t.Helper()
	const wantExit = 6
	home, id, _ := seedAttempt(t, 1, func(string) any {
		doc := map[string]any{
			"attempts": map[string]any{
				"0": map[string]any{
					"output":    "tokens=23\n",
					"exit_code": wantExit,
				},
			},
		}
		if pty {
			doc["needs_pty"] = true
		}
		return doc
	})

	ctx := context.Background()
	var cancel context.CancelFunc
	if cancelRun {
		ctx, cancel = context.WithCancel(context.Background())
		defer cancel()
	}
	mgr := newBlockingUntilDoneManager()
	errc := make(chan error, 1)
	go func() {
		errc <- host.Run(host.WithWorkspace(ctx, mgr), home, id)
	}()

	var cancelAt time.Time
	select {
	case <-mgr.entered:
		if cancelRun {
			cancelAt = time.Now()
			cancel()
		}
	case err := <-errc:
		t.Fatalf("Run returned before Commit blocked: %v", err)
	case <-time.After(8 * time.Second):
		t.Fatal("Commit was not entered")
	}

	var runErr error
	select {
	case runErr = <-errc:
	case <-time.After(8 * time.Second):
		t.Fatal("Run did not finish after the commit deadline")
	}
	if runErr != nil {
		t.Fatalf("Run failed after the commit deadline: %v", runErr)
	}

	calls, cancelledAtEntry, hasDL, remain, commitErr, waited, doneAt := mgr.snapshot()
	if calls != 1 || cancelledAtEntry || !hasDL {
		t.Fatalf("commit calls=%d cancelledAtEntry=%v hasDeadline=%v", calls, cancelledAtEntry, hasDL)
	}
	if remain <= 0 || remain > bound+100*time.Millisecond {
		t.Fatalf("commit deadline remain %s, want within test bound %s", remain, bound)
	}
	if !errors.Is(commitErr, context.DeadlineExceeded) {
		t.Fatalf("commit err %v", commitErr)
	}
	if waited < bound/2 {
		t.Fatalf("commit returned in %s without exhausting its deadline", waited)
	}
	if cancelRun && !doneAt.After(cancelAt.Add(bound/2)) {
		t.Fatalf("commit finished at %s, run cancelled at %s", doneAt, cancelAt)
	}

	logb, err := os.ReadFile(filepath.Join(home, "attempts", id, "output.log"))
	if err != nil {
		t.Fatal(err)
	}
	attempt, events := loadAttempt(t, home, id)
	if attempt.Exit == nil || *attempt.Exit != wantExit || attempt.EndedAt.IsZero() {
		t.Fatalf("exit %#v ended %v", attempt.Exit, attempt.EndedAt)
	}
	wantUsage := runner.Fake{}.ParseUsage(logb)
	if attempt.Usage != wantUsage || wantUsage.Tokens == 0 {
		t.Fatalf("usage %+v want %+v log %q", attempt.Usage, wantUsage, logb)
	}
	if attempt.HeadCommit != "" {
		t.Fatalf("head %q", attempt.HeadCommit)
	}
	if _, ok := eventType(events, "host_finish"); !ok {
		t.Fatal("missing host_finish")
	}
	ev, ok := eventType(events, "workspace_commit")
	if !ok || !strings.Contains(string(ev.Data), context.DeadlineExceeded.Error()) {
		t.Fatalf("commit event %s", ev.Data)
	}
}
