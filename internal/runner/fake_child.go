package runner

import (
	"bytes"
	"context"
	"fmt"
	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/proc"
	"github.com/OttoPrua/Cardex/internal/strongcustody"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	var actualSHA string
	if len(os.Args) > 1 && os.Args[1] == strongFakeBootstrap {
		want := os.Getenv(strongFakeBundleEnv)
		body, err := strongcustody.ReadFakeDelivery(spec.PromptPath, "input.json", model.StrongBundleMax)
		if err != nil || len(body) == 0 || !model.StrongDigestKey(want) || model.StrongArtifactDigest(body) != want {
			return 1
		}
		if len(os.Args) != 6 || os.Args[2] != "--attempt" || os.Args[4] != "--index" || os.Args[5] != "0" ||
			spec.ScriptPath != "" || !strongFakeSafePath(spec.PromptPath) || !strongFakeSafePath(spec.WorkDir) {
			return 1
		}
		script, err := io.ReadAll(io.LimitReader(os.Stdin, strongFakeScriptMax+1))
		scriptSHA := os.Getenv(strongFakeScriptSHAEnv)
		if err != nil || !model.StrongDigestKey(scriptSHA) || model.StrongArtifactDigest(script) != scriptSHA || validateStrongFakeScript(script) != nil {
			return 1
		}

		spec.Script = script
		spec.strongFixtureInput = body

		challenge := os.Getenv(strongFakeChallengeEnv)
		if !model.StrongDigestKey(challenge) || !model.StrongKey(spec.AttemptID) {
			return 1
		}
		actualSHA = strongFakeProof(want, challenge, spec.AttemptID, scriptSHA)
	}

	res, err := Fake{}.run(context.Background(), spec, true)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if actualSHA != "" {
		fmt.Fprintln(os.Stdout, strongFakeBundlePrefix+actualSHA)
	}

	if _, err := os.Stdout.Write(res.Output); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if res.TailAfter > 0 {
		time.Sleep(res.TailAfter)
	}
	if len(res.Tail) > 0 {
		if _, err := os.Stdout.Write(res.Tail); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	if res.Hold > 0 {
		time.Sleep(res.Hold)
	}
	return res.ExitCode
}

func childPrelude(out *bytes.Buffer, spec LaunchSpec, step scriptAttempt) error {
	if stdoutIsTTY() {
		out.WriteString("tty=1\n")
	} else {
		out.WriteString("tty=0\n")
	}
	if spec.PromptPath != "" && len(spec.strongFixtureInput) == 0 {
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
	cmd := proc.Shell(context.Background(), "sleep "+shQuote(sec)+"; echo late > "+shQuote(filepath.ToSlash(step.GrandchildMarker)))
	var devNull *os.File
	if step.GrandchildDetach {
		// Close or redirect the descendant's stdio so the parent's output
		// pipe ends while the process group is still alive.
		f, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
		if err != nil {
			return err
		}
		devNull = f
		cmd.Stdin = f
		cmd.Stdout = f
		cmd.Stderr = f
	} else {
		// Keep the host's output pipe open after this process exits. Go would
		// otherwise point a nil Stdout at DevNull, and the pipe would close early.
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}
	if err := cmd.Start(); err != nil {
		if devNull != nil {
			_ = devNull.Close()
		}
		return err
	}
	if devNull != nil {
		_ = devNull.Close()
	}
	return os.WriteFile(filepath.Join(spec.WorkDir, "grandchild.pid"), []byte(strconv.Itoa(cmd.Process.Pid)), 0o644)
}

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
