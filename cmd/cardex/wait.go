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

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/store"
)

const waitPollInterval = 500 * time.Millisecond

func cmdWait(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("wait", "block until a card, or an objective or one of its cards, reaches a requested status", "<id> --until <statuses> --timeout <duration> --origin <kind:id> --json", stdout, stderr)
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
	wanted, err := parseUntil(*until)
	if err != nil {
		fmt.Fprintln(stderr, err)
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
	origin := readOrigin(fs)
	deadline := time.Now().Add(dur)
	last, err := watchOnce(ctx, db, id, wanted, origin != "")
	if errors.Is(err, sql.ErrNoRows) {
		fmt.Fprintf(stderr, "wait: %s not found\n", id)
		return 2
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if origin != "" {
		ids := last.cardIDs
		if last.obj.ID == "" {
			ids = []string{last.card.ID}
		}
		noteReads(db, origin, "wait", ids)
	}
	reason := ""
	for !wanted[last.status()] {
		remain := time.Until(deadline)
		if ctx.Err() != nil {
			reason = "interrupted"
			break
		}
		if remain <= 0 {
			reason = "timeout"
			break
		}
		timer := time.NewTimer(min(waitPollInterval, remain))
		select {
		case <-ctx.Done():
			timer.Stop()
			reason = "interrupted"
		case <-timer.C:
			var next waitState
			queryCtx, cancel := context.WithTimeout(ctx, waitPollInterval)
			next, err = watchOnce(queryCtx, db, id, wanted, false)
			cancel()
			if err == nil {
				last = next
			} else if ctx.Err() != nil {
				reason = "interrupted"
			} else if errors.Is(err, context.DeadlineExceeded) {
				continue
			} else {
				fmt.Fprintln(stderr, err)
				return 1
			}
		}
		if reason != "" {
			break
		}
	}
	snapshotCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	pulse, err := last.pulse(snapshotCtx, db)
	if err != nil && reason == "" {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if reason != "" {
		pulse.Reason = reason
	}
	writePulse(stdout, asJSON, pulse)
	if reason != "" {
		return 1
	}
	return 0
}

func parseUntil(raw string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		status := strings.ToLower(strings.TrimSpace(part))
		if status == "" {
			continue
		}
		if !model.CardStatus(status).Valid() && !model.ObjectiveStatus(status).Valid() {
			return nil, fmt.Errorf("unknown wait status %q", status)
		}
		out[status] = true
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("--until requires a comma-separated status list")
	}
	return out, nil
}

type waitState struct {
	card    model.Card
	obj     model.Objective
	cardIDs []string
}

func (s waitState) status() string {
	if s.card.ID != "" {
		return string(s.card.Status)
	}
	return string(s.obj.Status)
}

func (s waitState) pulse(ctx context.Context, db *store.Store) (store.Pulse, error) {
	if s.card.ID != "" {
		return db.CardPulse(ctx, s.card)
	}
	return db.ObjectivePulse(ctx, s.obj)
}

func watchOnce(ctx context.Context, db *store.Store, id string, wanted map[string]bool, collectIDs bool) (waitState, error) {
	card, err := db.GetCard(ctx, id)
	if err == nil || !errors.Is(err, sql.ErrNoRows) {
		return waitState{card: card}, err
	}
	obj, err := db.GetObjective(ctx, id)
	if err != nil {
		return waitState{}, err
	}
	state := waitState{obj: obj}
	objectiveMatched := wanted[string(obj.Status)]
	if objectiveMatched && !collectIDs {
		return state, nil
	}
	cards, err := db.ListCards(ctx, obj.ID)
	if err != nil {
		return waitState{}, err
	}
	for _, card := range cards {
		if collectIDs {
			state.cardIDs = append(state.cardIDs, card.ID)
		}
		if !objectiveMatched && state.card.ID == "" && wanted[string(card.Status)] {
			state.card = card
			if !collectIDs {
				break
			}
		}
	}
	return state, nil
}

func writePulse(w io.Writer, asJSON bool, pulse store.Pulse) {
	if asJSON {
		_ = json.NewEncoder(w).Encode(pulse)
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
	if origin == "" || len(cards) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = db.AddReads(ctx, origin, command, cards)
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
