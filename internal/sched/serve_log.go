package sched

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	serveHeartbeat  = time.Minute
	serveFaultLimit = 256 // distinct fingerprints retained per serve instance
	// This namespace is independent of serve-loop.log and all other historical logs.
	serveLogDir      = "serve-loop-v2"
	serveSegmentMark = "# cardex scheduler log v2\n"
	serveSegmentSize = 256 * 1024
	serveSegments    = 4 // active plus up to three previous segments
)

type serveEventKey struct{}

type serveReporter struct {
	event func(string)
	fault func(serveFault)
}

type serveFault struct {
	stage string
	cause [sha256.Size]byte
}

func serveReport(ctx context.Context) *serveReporter {
	if ctx == nil {
		return nil
	}
	reporter, _ := ctx.Value(serveEventKey{}).(*serveReporter)
	return reporter
}

func reportServeEvent(ctx context.Context, event string) {
	if reporter := serveReport(ctx); reporter != nil {
		reporter.event(event)
	}
}

// Only known scheduler stages may go to operational logs. Error text is
// hashed for in-memory deduplication only; never send it to a sink.
func reportServeError(ctx context.Context, stage string, cause error) {
	reporter := serveReport(ctx)
	if reporter == nil || cause == nil {
		return
	}
	switch stage {
	case "reconcile", "store", "harvest", "advance", "merge", "ranking",
		"dispatch", "schedules", "notify", "lifecycle", "design", "launch":
	default:
		stage = "tick"
	}
	reporter.fault(serveFault{
		stage: stage,
		cause: sha256.Sum256([]byte(cause.Error())),
	})
}

// A repeated fault can update health every tick but is written at most once
// per tickErrorWindow while its fingerprint is retained. Bound the table even
// if scheduler faults are dominated by changing IDs or error strings.
type serveFaultLimiter map[serveFault]time.Time

func (last serveFaultLimiter) allow(now time.Time, fault serveFault) bool {
	if previous, ok := last[fault]; ok {
		if now.Sub(previous) < tickErrorWindow {
			return false
		}
	} else if len(last) >= serveFaultLimit {
		var oldest serveFault
		var at time.Time
		for key, seen := range last {
			if now.Sub(seen) >= tickErrorWindow {
				delete(last, key)
			} else if at.IsZero() || seen.Before(at) {
				at, oldest = seen, key
			}
		}
		if len(last) >= serveFaultLimit {
			delete(last, oldest)
		}
	}
	last[fault] = now
	return true
}

type serveJournal struct {
	home     string
	root     *os.Root
	file     *os.File
	fileName string
	size     int64
	maxSize  int64
	maxFiles int
}

func (j *serveJournal) append(record string) error {
	needed := int64(len(record))
	if needed+int64(len(serveSegmentMark)) > j.maxSize {
		return errors.New("scheduler record exceeds segment limit")
	}
	prefix := ""
	if j.file == nil || j.size+needed > j.maxSize {
		if err := j.rotate(); err != nil {
			return err
		}
		prefix = serveSegmentMark
	}
	n, err := j.file.WriteString(prefix + record)
	j.size += int64(n)
	if err == nil {
		return nil
	}
	// Do not strand an unrecognizable empty/partial marker in the
	// namespace if the first record could not be written.
	info, _ := j.file.Stat()
	name := j.fileName
	j.closeSegment()
	if prefix != "" && info != nil && n < len(prefix) {
		if current, statErr := j.root.Lstat(name); statErr == nil && os.SameFile(info, current) {
			_ = j.root.Remove(name)
		}
	}
	return err
}

func (j *serveJournal) closeSegment() {
	if j.file != nil {
		_ = j.file.Close()
		j.file = nil
	}
	j.fileName = ""
	j.size = 0
}

func (j *serveJournal) close() {
	j.closeSegment()
	if j.root != nil {
		_ = j.root.Close()
	}
}

func (j *serveJournal) rotate() error {
	j.closeSegment()
	if j.root == nil {
		path := filepath.Join(j.home, serveLogDir)
		if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return errors.New("scheduler log namespace is not a directory")
		}
		root, err := os.OpenRoot(path)
		if err != nil {
			return err
		}
		now, err := root.Stat(".")
		if err != nil || !os.SameFile(info, now) {
			_ = root.Close()
			return errors.New("scheduler log directory changed during open")
		}
		j.root = root
	}

	dir, err := j.root.Open(".")
	if err != nil {
		return err
	}
	names, err := dir.Readdirnames(-1)
	_ = dir.Close()
	if err != nil {
		return err
	}
	var known []string
	var highest uint64
	for _, name := range names {
		num, ok := segmentNumber(name)
		if !ok {
			continue
		}
		if num > highest {
			highest = num
		}
		if j.ownsSegment(name) {
			known = append(known, name)
		}
	}
	if highest == ^uint64(0) {
		return errors.New("scheduler log sequence exhausted")
	}
	sort.Strings(known)
	for len(known) >= j.maxFiles {
		name := known[0]
		known = known[1:]
		if !j.ownsSegment(name) {
			continue
		}
		if err := j.root.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	// Exclusive creation is important: never open a pre-existing file or
	// link for writing, even if an unrelated entry looks like our segment.
	name := segmentName(highest + 1)
	file, err := j.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	j.file = file
	j.fileName = name
	return nil
}

func segmentName(n uint64) string { return fmt.Sprintf("segment-%020d.log", n) }

func segmentNumber(name string) (uint64, bool) {
	const prefix, suffix = "segment-", ".log"
	if len(name) != len(prefix)+20+len(suffix) || !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
		return 0, false
	}
	n, err := strconv.ParseUint(name[len(prefix):len(prefix)+20], 10, 64)
	return n, err == nil && name == segmentName(n)
}

func (j *serveJournal) ownsSegment(name string) bool {
	info, err := j.root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	file, err := j.root.Open(name)
	if err != nil {
		return false
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return false
	}
	var mark [len(serveSegmentMark)]byte
	_, err = io.ReadFull(file, mark[:])
	return err == nil && string(mark[:]) == serveSegmentMark
}
