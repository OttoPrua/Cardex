package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/OttoPrua/Cardex/internal/notify"
)

const notifyGroupUsage = `notify — register who receives questions
purpose: add, list, remove, or test a notify receiver in this home

parameters:
  add     --id --adapter --target [--root] [--project] [--objective] [--dir] [--all] [--events] [--enabled] [--from-origin] [--origin]
  list
  remove  <id>
  test    <id> [--wait]
`

func cmdNotify(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	if groupHelp(stdout, args, notifyGroupUsage) {
		return 0
	}
	switch args[0] {
	case "add":
		return notifyAdd(home, asJSON, args[1:], stdout, stderr)
	case "list":
		return notifyList(home, asJSON, args[1:], stdout, stderr)
	case "remove":
		return notifyRemove(home, args[1:], stdout, stderr)
	case "test":
		return notifyTest(home, args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown notify verb %q\n", args[0])
		return 2
	}
}

func notifyAdd(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("notify add", "register one receiver. a duplicate id is refused and the file is left unchanged", "--id", stdout, stderr)
	id := fs.String("id", "", "receiver id")
	adapter := fs.String("adapter", "", "codex, hermes, lark, or file")
	target := fs.String("target", "", "codex thread, hermes profile[/session], lark chat, or file path")
	root := fs.String("root", "", "hermes inbox root; default is this home")
	var projects, objectives, dirs, events stringList
	fs.Var(&projects, "project", "scope project; repeat or separate with commas")
	fs.Var(&objectives, "objective", "scope objective; repeat or separate with commas")
	fs.Var(&dirs, "dir", "scope directory prefix; repeat or separate with commas")
	all := fs.Bool("all", false, "subscribe to every project and objective")
	fs.Var(&events, "events", "question kinds; empty means all")
	enabled := fs.Bool("enabled", true, "receive deliveries")
	fromOrigin := fs.Bool("from-origin", false, "set adapter and target from --origin or CARDEX_ORIGIN")
	origin := fs.String("origin", "", "kind:id used by --from-origin; overrides CARDEX_ORIGIN")
	if code, ok := parseSet(fs, args); !ok {
		return code
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "notify add --id <id>")
		return 2
	}
	rec := notify.Receiver{
		ID:      strings.TrimSpace(*id),
		Adapter: strings.TrimSpace(*adapter),
		Target:  strings.TrimSpace(*target),
		Root:    strings.TrimSpace(*root),
		Scope: notify.Scope{
			Projects:    []string(projects),
			Objectives:  []string(objectives),
			DirPrefixes: []string(dirs),
			All:         *all,
		},
		Events:  []string(events),
		Enabled: *enabled,
	}
	if *fromOrigin {
		raw := strings.TrimSpace(*origin)
		if raw == "" {
			raw = os.Getenv("CARDEX_ORIGIN")
		}
		derived, err := notify.ReceiverFromOrigin(raw)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		if rec.Adapter == "" {
			rec.Adapter = derived.Adapter
		}
		if rec.Target == "" {
			rec.Target = derived.Target
		}
	}
	dir, err := resolveHome(home)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if err := notify.AddReceiver(dir, rec); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if asJSON {
		return writeJSON(stdout, rec)
	}
	fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\n", rec.ID, rec.Adapter, rec.Target, enabledWord(rec.Enabled))
	return 0
}

func notifyList(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("notify list", "list every receiver and whether it is enabled", "", stdout, stderr)
	if code, ok := parseSet(fs, args); !ok {
		return code
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "notify list")
		return 2
	}
	dir, err := resolveHome(home)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	receivers, warnings, err := notify.ListReceivers(dir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	for _, warning := range warnings {
		fmt.Fprintf(stderr, "warning: %s\n", warning.String())
	}
	if asJSON {
		if receivers == nil {
			receivers = []notify.Receiver{}
		}
		return writeJSON(stdout, receivers)
	}
	for _, rec := range receivers {
		fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\n", rec.ID, rec.Adapter, rec.Target, enabledWord(rec.Enabled))
	}
	return 0
}

func notifyRemove(home string, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("notify remove", "delete one receiver by id", "<id>", stdout, stderr)
	if code, ok := parseSet(fs, args); !ok {
		return code
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "notify remove <id>")
		return 2
	}
	dir, err := resolveHome(home)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	id := fs.Arg(0)
	if err := notify.RemoveReceiver(dir, id); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "removed %s\n", id)
	return 0
}

func notifyTest(home string, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("notify test", "send one test message and report whether it was received", "<id>", stdout, stderr)
	wait := fs.Duration("wait", 15*time.Second, "how long a hermes test waits for the ack")
	if code, ok := parseSet(fs, moveFlag(args, "wait", true)); !ok {
		return code
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "notify test <id>")
		return 2
	}
	dir, err := resolveHome(home)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	id := fs.Arg(0)
	receivers, warnings, err := notify.ListReceivers(dir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	for _, warning := range warnings {
		fmt.Fprintf(stderr, "warning: %s\n", warning.String())
	}
	var rec notify.Receiver
	found := false
	for _, item := range receivers {
		if item.ID == id {
			rec = item
			found = true
			break
		}
	}
	if !found {
		fmt.Fprintf(stderr, "notify: receiver %s not found\n", id)
		return 1
	}
	if rec.Adapter == "hermes" {
		root, ackPath, wakeID, digest, err := notify.PrepareHermesTest(dir, rec, time.Now())
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "waiting %s\n", ackPath)
		if notify.WaitHermesAck(root, rec.ID, wakeID, digest, *wait) {
			fmt.Fprintf(stdout, "received %s\n", id)
			return 0
		}
		fmt.Fprintf(stderr, "not received %s\n", id)
		return 1
	}
	if err := notify.TestDeliver(context.Background(), notify.CommandExec, dir, rec); err != nil {
		fmt.Fprintf(stderr, "not received %s: %v\n", id, err)
		return 1
	}
	fmt.Fprintf(stdout, "received %s\n", id)
	return 0
}

// moveFlag lets one valued flag sit after the positional id.
// The flag parser stops at the first non-flag, and the usage lists <id> first.
func moveFlag(args []string, name string, takesValue bool) []string {
	var flags, rest []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--"+name || arg == "-"+name {
			flags = append(flags, arg)
			if takesValue && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		if strings.HasPrefix(arg, "--"+name+"=") || strings.HasPrefix(arg, "-"+name+"=") {
			flags = append(flags, arg)
			continue
		}
		rest = append(rest, arg)
	}
	return append(flags, rest...)
}

func enabledWord(on bool) string {
	if on {
		return "enabled"
	}
	return "disabled"
}

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }

func (s *stringList) Set(raw string) error {
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			*s = append(*s, part)
		}
	}
	return nil
}
