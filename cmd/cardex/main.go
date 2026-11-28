package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/OttoPrua/Cardex/internal/homeguard"
	"github.com/OttoPrua/Cardex/internal/proc"
	"github.com/OttoPrua/Cardex/internal/proxy"
	"github.com/OttoPrua/Cardex/internal/sched"
	"github.com/OttoPrua/Cardex/internal/store"
)

const usage = `cardex — v2 command surface

commands:
  migrate tiers --yes   explicitly activate the tier convention on an old store
  setup [--yes]          first run: find CLIs, check sign-in, add channels
  serve
  project add|list|show|set
  objective add|list|show|brief|set|pause|resume|cancel
  node list|show|freeze|reopen
  card add|import|list|show|cancel|retry|pause|resume|revive|absorb|recover|merged
  wait <card-id|objective-id> --until <statuses> --timeout <duration>
  reads
  blockers [--objective <id>]
  research report       inspect/export synthetic workflow receipts offline
  verify <card-id>       verify recorded evidence for the latest attempt
  question list|show|answer
  channel list|show|set|check
  route explain <card>
  positioning show|propose|proposals|approve|reject
  schedule list|run <name>
  scorecard
  import-legacy [--dry-run] <v1-home>
  notify add|list|remove|test
  proxy show|check
  doctor
  snapshot --json [--card <id>] [--checkpointed-copy]
  version
  host <attempt-id>
`

func main() {
	if code, ok := runLiveSnapshotWorker(os.Args[1:]); ok {
		os.Exit(code)
	}
	if err := ensureRuntimePath(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Exit(Main(os.Args[1:], os.Stdout, os.Stderr))
}

// Main runs one cardex invocation.
func Main(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stdout, usage)
		return 0
	}
	home, asJSON, rest, err := splitGlobal(args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if isGlobalHelp(rest) {
		fmt.Fprint(stdout, usage)
		return 0
	}
	if rest[0] == "snapshot" {
		return cmdSnapshot(home, asJSON, rest[1:], stdout, stderr)
	}
	if rest[0] == "verify" {
		return cmdVerify(home, asJSON, rest[1:], stdout, stderr)
	}
	if rest[0] == "doctor" {
		return cmdDoctor(home, rest[1:], stdout, stderr)
	}
	if rest[0] == "migrate" {
		return cmdMigrateTiers(home, asJSON, rest[1:], stdout, stderr)
	}
	// Import handles its own existing-home boundary, including dry-run.
	if len(rest) >= 2 && rest[0] == "card" && rest[1] == "import" {
		return cmdCard(home, asJSON, rest[1:], stdout, stderr)
	}
	if len(rest) > 1 && rest[0] == "channel" && rest[1] == "set" {
		// Parse model preview flags before the usual home/proxy setup:
		// that setup can create a home even if the preview is invalid.
		return channelSet(home, asJSON, rest[2:], stdout, stderr)
	}
	// Offline fixture calculations must not touch any home, DB, engine
	// config, credential, or configured proxy.
	if isOfflineChannelQuote(rest) {
		return channelQuoteFixture(rest[2:], stdout, stderr)
	}
	if rest[0] == "research" {
		return cmdResearch(home, rest[1:], stdout, stderr)
	}
	applyCommandProxy(home, stderr)
	switch rest[0] {
	case "proxy":
		return cmdProxy(home, asJSON, rest[1:], stdout, stderr)
	case "serve":
		return cmdServe(home, asJSON, rest[1:], stdout, stderr)
	case "project":
		return cmdProject(home, asJSON, rest[1:], stdout, stderr)
	case "objective":
		return cmdObjective(home, asJSON, rest[1:], stdout, stderr)
	case "card":
		return cmdCard(home, asJSON, rest[1:], stdout, stderr)
	case "wait":
		return cmdWait(home, asJSON, rest[1:], stdout, stderr)
	case "reads":
		return cmdReads(home, asJSON, rest[1:], stdout, stderr)
	case "blockers":
		return cmdBlockers(home, asJSON, rest[1:], stdout, stderr)
	case "node":
		return cmdNode(home, asJSON, rest[1:], stdout, stderr)
	case "question":
		return cmdQuestion(home, asJSON, rest[1:], stdout, stderr)
	case "channel":
		return cmdChannel(home, asJSON, rest[1:], stdout, stderr)
	case "route":
		return cmdRoute(home, asJSON, rest[1:], stdout, stderr)
	case "positioning":
		return cmdPositioning(home, asJSON, rest[1:], stdout, stderr)
	case "scorecard":
		return cmdScorecard(home, rest[1:], stdout, stderr)
	case "schedule":
		return cmdSchedule(home, asJSON, rest[1:], stdout, stderr)
	case "import-legacy":
		return cmdImport(home, rest[1:], stdout, stderr)
	case "notify":
		return cmdNotify(home, asJSON, rest[1:], stdout, stderr)
	case "version", "--version", "-v":
		return cmdVersion(home, asJSON, stdout)
	case "setup":
		return cmdSetup(home, asJSON, rest[1:], stdout, stderr)
	case "host":
		return cmdHost(home, rest[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s", rest[0], usage)
		return 2
	}
}

func applyCommandProxy(home string, stderr io.Writer) {
	if dir, err := resolveHome(home); err == nil {
		// One proxy entry for the serve, its git and notify calls, and every
		// executor it starts. No "proxy" section in config.json changes nothing.
		if err := proxy.ApplyProcess(dir); err != nil {
			fmt.Fprintln(stderr, "warning:", err)
		}
	}
}

// isGlobalHelp is true only when there is no command, or the command itself is help.
// A later --help belongs to that command.
func isGlobalHelp(rest []string) bool {
	if len(rest) == 0 {
		return true
	}
	switch rest[0] {
	case "help", "--help", "-h":
		return true
	default:
		return false
	}
}

func splitGlobal(args []string) (home string, asJSON bool, rest []string, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			return home, asJSON, append(rest, args[i:]...), nil
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

func cmdServe(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("serve", "run the board and the scheduler loop until interrupted", "--addr", stdout, stderr)
	addr := fs.String("addr", "127.0.0.1:0", "listen address")
	if code, ok := parseSet(fs, args); !ok {
		return code
	}
	if refuseManagedRuntimeStart(stderr) {
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runServe(ctx, home, *addr, asJSON, stdout, stderr)
}

func runServe(ctx context.Context, home, addr string, asJSON bool, stdout, stderr io.Writer) int {
	home, err := resolveHome(home)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	// sched.Serve is the production loop. It builds the runner with sched.New
	// and writes the listening line when WithLog is set.
	err = sched.Serve(sched.WithLog(ctx, serveLog(stdout, asJSON, home)), home, addr)
	if err != nil && ctx.Err() == nil {
		fmt.Fprintf(stderr, "tick error: %v\n", err)
		return 1
	}
	return 0
}

func resolveHome(home string) (string, error) {
	abs, err := homePath(home)
	if err != nil {
		return "", err
	}
	if err := homeguard.Check(abs); err != nil {
		return "", err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return "", err
	}
	return abs, nil
}

func homePath(home string) (string, error) {
	if home == "" {
		home = os.Getenv("CARDEX_HOME")
	}
	if home == "" {
		user, err := proc.UserHome()
		if err != nil {
			return "", err
		}
		home = filepath.Join(user, ".cardex")
	}
	return filepath.Abs(home)
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
