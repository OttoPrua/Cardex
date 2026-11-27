package positioning

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
)

// RankingDisplay is an observation, not a route input. A nonzero Version
// identifies published rows (which can be empty) rather than the config seed.
// The evidence hash binds a dated note to those exact rows.
type RankingDisplay struct {
	Published    bool             `json:"published"`
	Version      int              `json:"version"`
	EvidenceKey  string           `json:"evidence_key,omitempty"`
	EvidenceDate string           `json:"evidence_date,omitempty"`
	Freshness    string           `json:"freshness"`
	RankedModels []string         `json:"ranked_models"`
	PendingKnown bool             `json:"pending_known"`
	Pending      []PendingRanking `json:"pending"`
}

type PendingRanking struct {
	ID         string `json:"id"`
	EntryCount int    `json:"entry_count"`
}

// RankingStatus reads the approved database snapshot and the existing
// proposals separately. Neither the read nor an evidence note can publish a
// proposal, alter route eligibility, or create another proposal.
func (h *Home) RankingStatus(ctx context.Context, now time.Time) (RankingDisplay, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	out := RankingDisplay{Freshness: "unknown", RankedModels: []string{}, Pending: []PendingRanking{}}
	var rows []model.Positioning
	if err := h.db.WithTx(ctx, func(ctx context.Context) error {
		var err error
		rows, err = h.db.ListPositioning(ctx)
		if err != nil {
			return err
		}
		out.Version = maxVersion(rows)
		if out.Version == 0 {
			// Read the legacy marker on the same SQLite snapshot; Current's seed
			// fallback is deliberately not shown as approved by the board.
			out.Version, err = h.db.LegacyPositioningVersion(ctx, positioningMetaID)
		}
		return err
	}); err != nil {
		return out, err
	}

	out.Published = len(rows) > 0 || out.Version > 0
	visible := withoutVersionRow(rows)
	if out.Version > 0 {
		payload, err := json.Marshal(visible)
		if err != nil {
			return out, err
		}
		sum := sha256.Sum256(payload)
		out.EvidenceKey = hex.EncodeToString(sum[:])
		out.EvidenceDate, out.Freshness = h.rankingEvidence(out.Version, out.EvidenceKey, now)
	}
	names := map[string]bool{}
	for _, row := range visible {
		name := strings.TrimSpace(row.Model)
		if name != "" && !names[name] {
			out.RankedModels = append(out.RankedModels, name)
			names[name] = true
		}
	}
	sort.Strings(out.RankedModels)
	out.PendingKnown = h.readPendingRanking(&out.Pending)
	return out, nil
}

type rankingEvidenceFile struct {
	Version      int    `json:"version"`
	EvidenceKey  string `json:"evidence_key"`
	EvidenceDate string `json:"evidence_date"`
}

const (
	maxEvidenceBytes        = 1024
	maxProposalDisplayBytes = 64 << 10
	maxScannedProposals     = 512
)

func (h *Home) readPendingRanking(out *[]PendingRanking) bool {
	root, err := os.OpenRoot(h.dir)
	if err != nil {
		return false
	}
	defer root.Close()
	path := filepath.Join("positioning", "proposals")
	dir, err := root.OpenRoot(path)
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	if err != nil {
		return false
	}
	defer dir.Close()
	f, err := dir.Open(".")
	if err != nil {
		return false
	}
	defer f.Close()

	entries, err := f.ReadDir(maxScannedProposals + 1)
	if err != nil && err != io.EOF {
		return false
	}
	known := len(entries) <= maxScannedProposals
	if !known {
		entries = entries[:maxScannedProposals]
	}
	for _, ent := range entries {
		name := ent.Name()
		if ent.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		id := strings.TrimSuffix(name, ".json")
		if _, err := hex.DecodeString(id); err != nil || len(id) != 16 || id != strings.ToLower(id) {
			known = false
			continue
		}
		var p struct {
			ID      string            `json:"id"`
			Status  string            `json:"status"`
			Entries []json.RawMessage `json:"entries"`
		}
		if !readRankingJSON(dir, name, maxProposalDisplayBytes, &p) || p.ID != id {
			known = false
			continue
		}
		switch p.Status {
		case "", "proposed":
			*out = append(*out, PendingRanking{ID: id, EntryCount: len(p.Entries)})
		case "approved", "待复核": // The latter is assigned only after publication.
		default:
			known = false
		}
	}
	sort.Slice(*out, func(i, j int) bool { return (*out)[i].ID < (*out)[j].ID })
	return known
}

func (h *Home) rankingEvidence(version int, key string, now time.Time) (date, freshness string) {
	root, err := os.OpenRoot(h.dir)
	if err != nil {
		return "", "unknown"
	}
	defer root.Close()
	path := filepath.Join("positioning", "evidence", fmt.Sprintf("v%d.json", version))
	var metadata rankingEvidenceFile
	if !readRankingJSON(root, path, maxEvidenceBytes, &metadata) || metadata.Version != version || metadata.EvidenceKey != key {
		return "", "unknown"
	}
	return dateFreshness(metadata.EvidenceDate, now)
}

// Read only regular, confined files; cap each read even when a file grows
// after the preflight check. Nothing but safe display fields leaves the view.
func readRankingJSON(root *os.Root, path string, limit int64, dst any) bool {
	info, err := root.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return false
	}
	f, err := root.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || !os.SameFile(info, fi) {
		return false
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return false
	}
	after, err := f.Stat()
	return err == nil && fi.Size() == after.Size() && fi.ModTime().Equal(after.ModTime()) && json.Unmarshal(raw, dst) == nil
}

// Date-only observations use UTC calendar days. Exactly two weeks (14
// days) is not yet older than two weeks. Never substitute timestamps from
// proposal files or approvals for the actual date the evidence was observed.
func dateFreshness(date string, now time.Time) (string, string) {
	observed, err := time.Parse(time.DateOnly, date)
	if err != nil || observed.IsZero() || observed.Format(time.DateOnly) != date || now.IsZero() {
		return "", "unknown"
	}
	y, m, d := now.UTC().Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	if observed.After(today) {
		return "", "unknown"
	}
	if observed.AddDate(0, 0, 14).Before(today) {
		return date, "stale"
	}
	return date, "fresh"
}
