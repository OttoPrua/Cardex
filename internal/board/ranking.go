package board

import (
	"context"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/OttoPrua/Cardex/internal/positioning"
	"github.com/OttoPrua/Cardex/internal/store"
)

type rankingView struct {
	positioning.RankingDisplay
	PublishedKnown bool     `json:"published_known"`
	MissingModels  []string `json:"missing_models"`
}

func loadRanking(ctx context.Context, home string, db *store.Store, channels []channelView, now time.Time) *rankingView {
	status, err := positioning.Inspect(home, db).RankingStatus(ctx, now)
	view := &rankingView{RankingDisplay: status, PublishedKnown: err == nil, MissingModels: []string{}}
	if err != nil {
		return view
	}
	seen := map[string]bool{}
	for _, name := range status.RankedModels {
		seen[name] = true
	}
	for _, ch := range channels {
		for _, name := range ch.Models {
			name = strings.TrimSpace(name)
			if name != "" && !seen[name] {
				view.MissingModels = append(view.MissingModels, name)
				seen[name] = true
			}
		}
	}
	sort.Strings(view.MissingModels)
	return view
}

// The normal channels page lazily opens a write-capable store which can
// initialize/migrate a home. Ranking never calls that opener.
func serveRanking(w http.ResponseWriter, r *http.Request, asJSON bool) {
	view := &channelsView{Tab: "ranking", Channels: []channelView{}, Models: []modelActivity{}, Ranking: unknownRanking()}
	if home, err := resolveHome(); err == nil {
		var ranking *rankingView
		ctx, now := r.Context(), time.Now()
		err := store.Inspect(filepath.Join(home, "cardex.db"), func(db *store.Store) {
			stored, err := db.ListChannels(ctx)
			if err != nil {
				return
			}
			channels := make([]channelView, 0, len(stored))
			for _, c := range stored {
				channels = append(channels, channelView{Models: c.Models})
			}
			ranking = loadRanking(ctx, home, db, channels, now)
		})
		if err == nil && ranking != nil {
			view.Ranking = ranking
		}
	}
	if asJSON {
		writeJSON(w, view)
		return
	}
	render(w, pageData{Title: "排名与证据", View: "status", Channels: view})
}

func unknownRanking() *rankingView {
	return &rankingView{RankingDisplay: positioning.RankingDisplay{Freshness: "unknown", RankedModels: []string{}, Pending: []positioning.PendingRanking{}}, MissingModels: []string{}}
}
