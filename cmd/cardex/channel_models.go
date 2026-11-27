package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"github.com/OttoPrua/Cardex/internal/store"
)

// modelChangeArgs detects explicit edit flags even after a positional id,
// without mistaking a value of an ordinary legacy flag for a preview flag.
func modelChangeArgs(fs *flag.FlagSet, args []string) bool {
	for i := 0; i < len(args) && args[i] != "--"; i++ {
		arg := args[i]
		name := flagToken(arg)
		switch name {
		case "dry-run", "if-models", "add-model", "remove-model":
			return true
		}
		if f := fs.Lookup(name); f != nil && !boolFlag(f) && !strings.Contains(arg, "=") {
			i++
		}
	}
	return false
}

type modelEdit struct {
	replace []string
	add     []string
	remove  []string
}

type channelModelPreview struct {
	Channel  string   `json:"channel"`
	Mode     string   `json:"mode"`
	Before   []string `json:"before"`
	After    []string `json:"after"`
	Added    []string `json:"added"`
	Removed  []string `json:"removed"`
	IfModels string   `json:"if_models,omitempty"`
	DryRun   bool     `json:"dry_run"`
}

func (edit modelEdit) validate() error {
	if len(edit.replace) == 0 && len(edit.add) == 0 && len(edit.remove) == 0 {
		return errors.New("model preview or edit requires --model, --add-model, or --remove-model")
	}
	if len(edit.replace) > 0 && (len(edit.add) > 0 || len(edit.remove) > 0) {
		return errors.New("--model replacement cannot be mixed with --add-model or --remove-model")
	}
	for _, group := range [][]string{edit.replace, edit.add, edit.remove} {
		for _, name := range group {
			if name == "" || name != strings.TrimSpace(name) || strings.ContainsFunc(name, unicode.IsControl) {
				return errors.New("model name must be non-empty and cannot contain surrounding whitespace or control characters")
			}
		}
	}
	for _, name := range edit.remove {
		if slices.Contains(edit.add, name) {
			return fmt.Errorf("cannot add and remove model %q in the same operation", name)
		}
	}
	return nil
}

func modelDifference(left, right []string) []string {
	out := []string{}
	excluded := make(map[string]bool, len(right))
	for _, name := range right {
		excluded[name] = true
	}
	for _, name := range left {
		if !excluded[name] {
			out = append(out, name)
			excluded[name] = true
		}
	}
	return out
}

func (edit modelEdit) plan(dir string, current store.ChannelModelState, dryRun bool) (channelModelPreview, error) {
	out := channelModelPreview{
		Channel: current.ID,
		Mode:    "edit",
		Before:  append([]string{}, current.Models...),
		After:   []string{},
		DryRun:  dryRun,
	}
	if len(edit.replace) > 0 {
		// Legacy --model preserves order and duplicates. Match that behavior.
		out.Mode = "replace"
		out.After = append(out.After, edit.replace...)
	} else {
		for _, name := range edit.remove {
			if !slices.Contains(current.Models, name) {
				return channelModelPreview{}, fmt.Errorf("cannot remove unknown model %q", name)
			}
		}
		for _, name := range current.Models {
			if !slices.Contains(edit.remove, name) {
				out.After = append(out.After, name)
			}
		}
		for _, name := range edit.add {
			if !slices.Contains(out.After, name) {
				out.After = append(out.After, name)
			}
		}
	}
	out.Added = modelDifference(out.After, out.Before)
	out.Removed = modelDifference(out.Before, out.After)
	if out.Mode == "replace" {
		return out, nil
	}
	// A preview token is a concurrency precondition, not a credential. Bind
	// the resolved home, channel identity, exact ordered before/after state,
	// and the v1 algorithm.
	raw, err := json.Marshal([]any{"channel-model-edit-v1", dir, current.ID, current.Runner, current.EngineProfile, out.Before, out.After})
	if err != nil {
		return channelModelPreview{}, err
	}
	sum := sha256.Sum256(raw)
	out.IfModels = hex.EncodeToString(sum[:])
	return out, nil
}

func emitChannelModelPreview(w io.Writer, asJSON bool, out channelModelPreview) int {
	if asJSON {
		return writeJSON(w, out)
	}
	label := "Model list updated"
	if slices.Equal(out.Before, out.After) {
		label = "No model changes needed"
	}
	if out.DryRun {
		label = "Preview only; no changes written"
	}
	fmt.Fprintf(w, "%s: channel %q\nbefore: %q\nafter: %q\nadded: %q\nremoved: %q\n", label, out.Channel, out.Before, out.After, out.Added, out.Removed)
	if out.DryRun {
		if len(out.After) == 0 {
			fmt.Fprintln(w, "The proposed list is empty; the channel will have no models.")
		}
		if out.Mode == "replace" {
			fmt.Fprintln(w, "To perform the legacy full replacement, repeat the --model options without --dry-run.")
		} else {
			fmt.Fprintf(w, "To apply an add/remove edit, repeat it without --dry-run and pass --if-models %s.\n", out.IfModels)
		}
	}
	return 0
}

func channelModelChange(home string, asJSON, dryRun bool, id, expected string, edit modelEdit, stdout, stderr io.Writer) int {
	if err := edit.validate(); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if dryRun && expected != "" {
		fmt.Fprintln(stderr, "--if-models is only for applying an add/remove preview")
		return 2
	}
	if !dryRun {
		if _, err := hex.DecodeString(expected); len(expected) != 64 || err != nil || expected != strings.ToLower(expected) || len(edit.replace) > 0 {
			fmt.Fprintln(stderr, "applying an add/remove edit requires --if-models from the same --dry-run preview")
			return 2
		}
	}
	dir, err := homePath(home)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	ctx := context.Background()
	current, err := store.PreviewChannelModels(ctx, filepath.Join(dir, "cardex.db"), id)
	if errors.Is(err, sql.ErrNoRows) {
		fmt.Fprintf(stderr, "channel %q does not exist\n", id)
		return 1
	}
	if err != nil {
		fmt.Fprintf(stderr, "cannot read channel model preview: %v\n", err)
		return 1
	}
	plan, err := edit.plan(dir, current, dryRun)
	if !dryRun {
		if err != nil || expected != plan.IfModels {
			fmt.Fprintln(stderr, "channel models or intended edit changed; run the same --dry-run preview again")
			return 1
		}
	} else if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if dryRun || slices.Equal(plan.Before, plan.After) {
		return emitChannelModelPreview(stdout, asJSON, plan)
	}
	db, err := openStoreForWrite(home)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer db.Close()
	err = db.WithWriteTx(ctx, func(tx context.Context) error {
		state, err := db.ChannelModels(tx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("channel was removed; run the preview again")
		}
		if err != nil {
			return err
		}
		if err := state.CheckIncrementalEdit(); err != nil {
			return err
		}
		actual, err := edit.plan(dir, state, false)
		if err != nil || actual.IfModels != expected {
			return errors.New("channel models or intended edit changed; run the same --dry-run preview again")
		}
		plan = actual
		return db.UpdateChannelModels(tx, id, actual.After)
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return emitChannelModelPreview(stdout, asJSON, plan)
}
