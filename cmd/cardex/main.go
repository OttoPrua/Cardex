package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"cardex/internal/sched"
	"cardex/internal/store"
)

const usage = `cardex — v2 command surface

commands:
  serve
  project add|list|show|set
  objective add|list|show|set|pause|resume|cancel
  node list|show|freeze|reopen
  card add|list|show|cancel|retry|pause|resume|revive
  question list|show|answer
  channel list|show|set|check
  route explain <card>
  positioning show|proposals|approve|reject
  schedule list|run <name>
  scorecard
  import-legacy [--dry-run] <v1-home>
  doctor
  host <attempt-id>
`

func main() {
	os.Exit(Main(os.Args[1:], os.Stdout, os.Stderr))
}

// Main runs one cardex invocation.
func Main(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || hasHelp(args) {
		fmt.Fprint(stdout, usage)
		return 0
	}
	home, asJSON, rest, err := splitGlobal(args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if len(rest) == 0 {
		fmt.Fprint(stdout, usage)
		return 0
	}
	switch rest[0] {
	case "serve":
		return cmdServe(home, rest[1:], stdout, stderr)
	case "project":
		return cmdProject(home, asJSON, rest[1:], stdout, stderr)
	case "objective":
		return cmdObjective(home, asJSON, rest[1:], stdout, stderr)
	case "card":
		return cmdCard(home, asJSON, rest[1:], stdout, stderr)
	case "node":
		return unimplemented(stdout, "R3-A")
	case "question":
		return unimplemented(stdout, "R3-A")
	case "channel":
		return unimplemented(stdout, "R2-D")
	case "route":
		return unimplemented(stdout, "R2-D")
	case "positioning":
		return unimplemented(stdout, "R4-C")
	case "scorecard":
		return unimplemented(stdout, "R4-C")
	case "schedule":
		return unimplemented(stdout, "R3-C")
	case "import-legacy":
		return unimplemented(stdout, "R4-B")
	case "doctor":
		return unimplemented(stdout, "R4-B")
	case "host":
		return unimplemented(stdout, "R2-A")
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s", rest[0], usage)
		return 2
	}
}

func hasHelp(args []string) bool {
	if len(args) > 0 && args[0] == "help" {
		return true
	}
	for _, a := range args {
		if a == "--help" || a == "-h" {
			return true
		}
	}
	return false
}

func splitGlobal(args []string) (home string, asJSON bool, rest []string, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--json":
			asJSON = true
		case a == "--home":
			if i+1 >= len(args) {
				return "", false, nil, errors.New("--home requires a value")
			}
			i++
			home = args[i]
		case strings.HasPrefix(a, "--home="):
			home = strings.TrimPrefix(a, "--home=")
		default:
			rest = append(rest, a)
		}
	}
	return home, asJSON, rest, nil
}

func unimplemented(stdout io.Writer, lane string) int {
	fmt.Fprintf(stdout, "未实现（由 %s 负责）\n", lane)
	return 0
}

func cmdServe(home string, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	addr := fs.String("addr", "127.0.0.1:0", "listen address")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runServe(ctx, home, *addr, stdout, stderr)
}

func runServe(ctx context.Context, home, addr string, stdout, stderr io.Writer) int {
	if reservedPort(addr) {
		fmt.Fprintln(stderr, "refusing reserved port")
		return 2
	}
	home, err := resolveHome(home)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	// Serve owns the listener and the tick loop. This loop copies its address
	// and iteration log onto the writer TestServeLoopsUntilStopped already watches.
	errc := make(chan error, 1)
	go func() {
		errc <- sched.Serve(ctx, home, addr)
	}()
	printed := false
	seen := 0
	for {
		if !printed {
			if b, rerr := os.ReadFile(filepath.Join(home, "serve-addr")); rerr == nil {
				fmt.Fprintf(stdout, "serve listening %s\n", strings.TrimSpace(string(b)))
				printed = true
			}
		}
		if b, rerr := os.ReadFile(filepath.Join(home, "serve-loop.log")); rerr == nil {
			n := strings.Count(string(b), "\n")
			for seen < n {
				seen++
				fmt.Fprintf(stdout, "serve stub-loop iteration %d\n", seen)
			}
		}
		select {
		case err := <-errc:
			if err == nil || ctx.Err() != nil || errors.Is(err, context.Canceled) {
				return 0
			}
			fmt.Fprintln(stderr, err)
			return 1
		case <-ctx.Done():
			select {
			case err := <-errc:
				if err != nil && !errors.Is(err, context.Canceled) {
					fmt.Fprintln(stderr, err)
					return 1
				}
			case <-time.After(3 * time.Second):
				fmt.Fprintln(stderr, "serve did not return")
				return 1
			}
			return 0
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func reservedPort(addr string) bool {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		port = addr
	}
	return port == "8787" || port == "8788"
}

func resolveHome(home string) (string, error) {
	if home == "" {
		home = os.Getenv("CARDEX_HOME")
	}
	if home == "" {
		return "", errors.New("missing --home or CARDEX_HOME")
	}
	abs, err := filepath.Abs(home)
	if err != nil {
		return "", err
	}
	user, err := os.UserHomeDir()
	if err == nil {
		banned := filepath.Join(user, ".cardex")
		if abs == banned || strings.HasPrefix(abs, banned+string(filepath.Separator)) {
			return "", errors.New("refusing to use ~/.cardex")
		}
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return "", err
	}
	return abs, nil
}

func openStore(home string) (*store.Store, error) {
	dir, err := resolveHome(home)
	if err != nil {
		return nil, err
	}
	return store.Open(filepath.Join(dir, "cardex.db"))
}

func writeJSON(w io.Writer, v any) int {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		fmt.Fprintln(w, err)
		return 1
	}
	return 0
}

func newID(prefix string) (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(b[:]), nil
}

type stringsFlag []string

func (s *stringsFlag) String() string { return strings.Join(*s, ",") }
func (s *stringsFlag) Set(v string) error {
	*s = append(*s, v)
	return nil
}
