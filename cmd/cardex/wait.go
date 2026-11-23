package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/OttoPrua/Cardex/internal/notify"
	"github.com/OttoPrua/Cardex/internal/store"
)

// waitPollInterval is the pause between database checks. The command sleeps
// instead of spinning.
const waitPollInterval = 200 * time.Millisecond

func cmdWait(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("wait", "block until a card, or an objective or one of its cards, reaches a requested status", "<id> --until <statuses> --timeout <duration> --origin --json", stdout, stderr)
	until := fs.String("until", "", "comma-separated statuses, such as merged,needs_input,failed,canceled,reviewing")
	timeout := fs.String("timeout", "", "how long to wait, for example 30s or 2m")
	acceptOrigin(fs)
	if code, ok := parseSet(fs, hoistFlags(fs, args)); !ok {
		return code
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "wait <card-id|objective-id> --until <statuses> --timeout <duration>")
		return 2
	}
	wanted := parseUntil(*until)
	if len(wanted) == 0 {
		fmt.Fprintln(stderr, "--until requires a comma-separated status list")
		return 2
	}
	dur, err := time.ParseDuration(strings.TrimSpace(*timeout))
	if err != nil || dur <= 0 {
		fmt.Fprintln(stderr, "--timeout requires a positive duration, for example 30s")
		return 2
	}
	db, err := openStore(home)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer db.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	id := fs.Arg(0)
	deadline := time.Now().Add(dur)
	counted := false
	var last notify.Pulse
	for {
		pulse, matched, found, err := watchOnce(ctx, db, id, wanted)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if !found {
			fmt.Fprintf(stderr, "wait: %s not found\n", id)
			return 2
		}
		if !counted {
			noteReads(db, resolvedOrigin(args), "wait", watchCards(ctx, db, id))
			counted = true
		}
		last = pulse
		if matched {
			writePulse(stdout, asJSON, pulse)
			return 0
		}
		remain := time.Until(deadline)
		if remain <= 0 || ctx.Err() != nil {
			last.Reason = "timeout"
			writePulse(stdout, asJSON, last)
			return 1
		}
		pause := waitPollInterval
		if remain < pause {
			pause = remain
		}
		timer := time.NewTimer(pause)
		select {
		case <-ctx.Done():
			timer.Stop()
			last.Reason = "timeout"
			writePulse(stdout, asJSON, last)
			return 1
		case <-timer.C:
		}
	}
}

func parseUntil(raw string) map[string]bool {
	out := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.ToLower(strings.TrimSpace(part))
		if part != "" {
			out[part] = true
		}
	}
	return out
}

func watchOnce(ctx context.Context, db *store.Store, id string, wanted map[string]bool) (notify.Pulse, bool, bool, error) {
	card, err := db.GetCard(ctx, id)
	if err == nil {
		pulse := notify.CardSnapshot(ctx, db, card)
		return pulse, wanted[strings.ToLower(pulse.Status)], true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return notify.Pulse{}, false, false, err
	}
	obj, err := db.GetObjective(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return notify.Pulse{}, false, false, nil
	}
	if err != nil {
		return notify.Pulse{}, false, false, err
	}
	if wanted[strings.ToLower(string(obj.Status))] {
		return notify.ObjectiveSnapshot(ctx, db, obj), true, true, nil
	}
	cards, err := db.ListCards(ctx, obj.ID)
	if err != nil {
		return notify.Pulse{}, false, false, err
	}
	for _, card := range cards {
		if wanted[strings.ToLower(string(card.Status))] {
			return notify.CardSnapshot(ctx, db, card), true, true, nil
		}
	}
	pulse := notify.ObjectiveSnapshot(ctx, db, obj)
	return pulse, false, true, nil
}

func watchCards(ctx context.Context, db *store.Store, id string) []string {
	if _, err := db.GetCard(ctx, id); err == nil {
		return []string{id}
	}
	cards, err := db.ListCards(ctx, id)
	if err != nil {
		return nil
	}
	ids := make([]string, 0, len(cards))
	for _, card := range cards {
		ids = append(ids, card.ID)
	}
	return ids
}

func writePulse(w io.Writer, asJSON bool, pulse notify.Pulse) {
	if asJSON {
		body, err := json.Marshal(pulse)
		if err != nil {
			fmt.Fprintln(w, err)
			return
		}
		fmt.Fprintf(w, "%s\n", body)
		return
	}
	fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
		oneLine(pulse.ID), oneLine(pulse.Status), oneLine(pulse.Reason),
		oneLine(pulse.Model), oneLine(pulse.SHA), oneLine(pulse.QuestionID))
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.ReplaceAll(s, "\t", " ")
}

func noteReads(db *store.Store, origin, command string, cards []string) {
	if db == nil || strings.TrimSpace(origin) == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for _, id := range cards {
		_ = db.AddRead(ctx, origin, id, command)
	}
}

func cmdReads(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("reads", "show management-side read counts recorded for --origin or CARDEX_ORIGIN", "[--origin <kind:id>] --json", stdout, stderr)
	originFlag := fs.String("origin", "", "only this origin; empty lists every origin")
	if code, ok := parseSet(fs, args); !ok {
		return code
	}
	origin := ""
	if flagSeen(fs, "origin") {
		origin = strings.TrimSpace(*originFlag)
	}
	db, err := openStore(home)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer db.Close()
	items, err := db.ListReads(context.Background(), origin)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	view := make([]readView, 0, len(items))
	for _, item := range items {
		view = append(view, readView{Origin: item.Origin, CardID: item.CardID, Command: item.Command, Count: item.Count})
	}
	if asJSON {
		return writeJSON(stdout, view)
	}
	for _, item := range view {
		fmt.Fprintf(stdout, "%s\t%s\t%s\t%d\n", item.Origin, item.CardID, item.Command, item.Count)
	}
	return 0
}

type readView struct {
	Origin  string `json:"origin"`
	CardID  string `json:"card_id"`
	Command string `json:"command"`
	Count   int    `json:"count"`
}
