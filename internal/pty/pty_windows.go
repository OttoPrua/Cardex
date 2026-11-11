//go:build windows

package pty

import (
	"errors"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Start runs cmd on a Windows pseudo console (ConPTY, Windows 10 1809+).
// exec.Cmd cannot attach a pseudo console, so the process is created directly;
// cmd.Process is filled in, and the caller waits with PTY.Wait.
func Start(cmd *exec.Cmd) (*PTY, error) {
	if cmd == nil {
		return nil, os.ErrInvalid
	}
	if cmd.Err != nil {
		return nil, cmd.Err
	}
	if cmd.Process != nil {
		return nil, errors.New("pty: command already started")
	}
	var inRead, inWrite, outRead, outWrite windows.Handle
	if err := windows.CreatePipe(&inRead, &inWrite, nil, 0); err != nil {
		return nil, err
	}
	if err := windows.CreatePipe(&outRead, &outWrite, nil, 0); err != nil {
		closeAll(inRead, inWrite)
		return nil, err
	}
	var hpc windows.Handle
	if err := windows.CreatePseudoConsole(windows.Coord{X: 200, Y: 50}, inRead, outWrite, 0, &hpc); err != nil {
		closeAll(inRead, inWrite, outRead, outWrite)
		return nil, err
	}
	// The pseudo console holds its own copies of these ends.
	closeAll(inRead, outWrite)

	pi, err := create(cmd, hpc)
	if err != nil {
		windows.ClosePseudoConsole(hpc)
		closeAll(inWrite, outRead)
		return nil, err
	}
	proc, err := os.FindProcess(int(pi.ProcessId))
	closeAll(pi.Thread, pi.Process)
	if err != nil {
		windows.ClosePseudoConsole(hpc)
		closeAll(inWrite, outRead)
		return nil, err
	}
	cmd.Process = proc
	m := &master{
		in:  os.NewFile(uintptr(inWrite), "conpty-in"),
		out: os.NewFile(uintptr(outRead), "conpty-out"),
		hpc: hpc,
	}
	p := &PTY{Master: m, Cmd: cmd}
	p.wait = func() error {
		state, err := proc.Wait()
		// conhost renders asynchronously: give it until output goes quiet
		// before closing, or the child's last output is dropped. The output
		// pipe only ends once the console is closed.
		m.waitQuiet(250*time.Millisecond, 3*time.Second)
		m.closeConsole()
		if err != nil {
			return err
		}
		cmd.ProcessState = state
		if !state.Success() {
			return &exec.ExitError{ProcessState: state}
		}
		return nil
	}
	return p, nil
}

func create(cmd *exec.Cmd, hpc windows.Handle) (*windows.ProcessInformation, error) {
	attrs, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return nil, err
	}
	defer attrs.Delete()
	// The attribute value is the HPCON itself, not a pointer to it.
	if err := attrs.Update(windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE,
		*(*unsafe.Pointer)(unsafe.Pointer(&hpc)), unsafe.Sizeof(hpc)); err != nil {
		return nil, err
	}
	si := &windows.StartupInfoEx{ProcThreadAttributeList: attrs.List()}
	si.Cb = uint32(unsafe.Sizeof(*si))

	app, err := windows.UTF16PtrFromString(cmd.Path)
	if err != nil {
		return nil, err
	}
	args := cmd.Args
	if len(args) == 0 {
		args = []string{cmd.Path}
	}
	line, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(args))
	if err != nil {
		return nil, err
	}
	var dir *uint16
	if cmd.Dir != "" {
		if dir, err = windows.UTF16PtrFromString(cmd.Dir); err != nil {
			return nil, err
		}
	}
	env := cmd.Env
	if env == nil {
		env = os.Environ()
	}
	block := envBlock(env)
	pi := new(windows.ProcessInformation)
	flags := uint32(windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_UNICODE_ENVIRONMENT)
	if cmd.SysProcAttr != nil {
		flags |= cmd.SysProcAttr.CreationFlags
	}
	if err := windows.CreateProcess(app, line, nil, nil, false, flags,
		&block[0], dir, &si.StartupInfo, pi); err != nil {
		return nil, &os.PathError{Op: "fork/exec", Path: cmd.Path, Err: err}
	}
	return pi, nil
}

// envBlock builds a sorted, de-duplicated (last wins, case-insensitive)
// UTF-16 environment block.
func envBlock(env []string) []uint16 {
	seen := map[string]int{}
	var kept []string
	for _, kv := range env {
		k := kv
		if i := strings.Index(kv[min(1, len(kv)):], "="); i >= 0 {
			k = kv[:i+min(1, len(kv))]
		}
		key := strings.ToUpper(k)
		if j, ok := seen[key]; ok {
			kept[j] = kv
			continue
		}
		seen[key] = len(kept)
		kept = append(kept, kv)
	}
	sort.SliceStable(kept, func(a, b int) bool { return strings.ToUpper(kept[a]) < strings.ToUpper(kept[b]) })
	var out []uint16
	for _, kv := range kept {
		out = append(out, utf16.Encode([]rune(kv))...)
		out = append(out, 0)
	}
	if len(out) == 0 {
		out = append(out, 0)
	}
	return append(out, 0)
}

type master struct {
	in, out   *os.File
	hpc       windows.Handle
	closeOnce sync.Once
	allOnce   sync.Once
	last      atomic.Int64 // UnixNano of the last read that returned data
}

// waitQuiet returns once no output has been read for quiet, or after max.
func (m *master) waitQuiet(quiet, max time.Duration) {
	start := time.Now()
	if m.last.Load() == 0 {
		m.last.Store(start.UnixNano())
	}
	for time.Since(start) < max {
		idle := time.Since(time.Unix(0, m.last.Load()))
		if idle >= quiet {
			return
		}
		time.Sleep(quiet - idle)
	}
}

func (m *master) Read(b []byte) (int, error) {
	n, err := m.out.Read(b)
	if n > 0 {
		m.last.Store(time.Now().UnixNano())
	}
	if err != nil && errors.Is(err, windows.ERROR_BROKEN_PIPE) {
		return n, os.ErrClosed
	}
	return n, err
}

func (m *master) Write(b []byte) (int, error) { return m.in.Write(b) }

func (m *master) closeConsole() {
	m.closeOnce.Do(func() { windows.ClosePseudoConsole(m.hpc) })
}

// Close tears down the console and both pipe ends.
func (m *master) Close() error {
	m.allOnce.Do(func() {
		_ = m.in.Close()
		// ClosePseudoConsole can block until output is drained on older
		// builds; closing our read end first lets it finish.
		_ = m.out.Close()
		m.closeConsole()
	})
	return nil
}

func closeAll(hs ...windows.Handle) {
	for _, h := range hs {
		if h != 0 && h != windows.InvalidHandle {
			_ = windows.CloseHandle(h)
		}
	}
}
