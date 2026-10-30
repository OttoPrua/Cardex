package runner

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

func init() {
	if os.Getenv("CARDEX_FAKE_CHILD") == "1" {
		os.Exit(fakeChildMain())
	}
}

func fakeChildMain() int {
	spec := LaunchSpec{
		WorkDir:    os.Getenv("CARDEX_FAKE_WORKDIR"),
		ScriptPath: os.Getenv("CARDEX_FAKE_SCRIPT"),
		PromptPath: os.Getenv("CARDEX_FAKE_PROMPT"),
	}
	for i := 1; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--attempt":
			if i+1 < len(os.Args) {
				i++
				spec.AttemptID = os.Args[i]
			}
		case "--index":
			if i+1 < len(os.Args) {
				i++
				n, err := strconv.Atoi(os.Args[i])
				if err != nil {
					fmt.Fprintln(os.Stderr, err)
					return 1
				}
				spec.AttemptIndex = n
			}
		}
	}
	res, err := Fake{}.run(context.Background(), spec, true)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if _, err := os.Stdout.Write(res.Output); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return res.ExitCode
}

func childPrelude(out *bytes.Buffer, spec LaunchSpec, step scriptAttempt) error {
	if stdoutIsTTY() {
		out.WriteString("tty=1\n")
	} else {
		out.WriteString("tty=0\n")
	}
	if spec.PromptPath != "" {
		fmt.Fprintf(out, "prompt=%s\n", spec.PromptPath)
	}
	if step.OutsideWrite != "" {
		f, err := os.OpenFile(step.OutsideWrite, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o644)
		if err != nil {
			fmt.Fprintf(out, "outside write denied: %v\n", err)
		} else {
			_ = f.Close()
			fmt.Fprintf(out, "outside write allowed\n")
		}
	}
	if step.GrandchildMarker == "" {
		return nil
	}
	after := step.GrandchildAfter
	if after == "" {
		after = "2s"
	}
	d, err := time.ParseDuration(after)
	if err != nil {
		return fmt.Errorf("fake grandchild: %w", err)
	}
	sec := strconv.FormatFloat(d.Seconds(), 'f', 3, 64)
	cmd := exec.Command("/bin/sh", "-c", `sleep "$1"; echo late > "$2"`, "sh", sec, step.GrandchildMarker)
	if err := cmd.Start(); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(spec.WorkDir, "grandchild.pid"), []byte(strconv.Itoa(cmd.Process.Pid)), 0o644)
}
