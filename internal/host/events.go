package host

import (
	"context"
	"encoding/json"
	"strings"

	"cardex/internal/model"
	"cardex/internal/store"
)

func appendEvent(ctx context.Context, db *store.Store, attemptID, kind string, data any) {
	raw, err := json.Marshal(data)
	if err != nil {
		return
	}
	_ = db.AppendEvent(ctx, &model.Event{
		Entity: "attempt:" + attemptID,
		Type:   kind,
		Data:   raw,
	})
}

func excerpt(output []byte, class model.ErrorClass) string {
	lines := strings.Split(string(output), "\n")
	picked := ""
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		if strings.Contains(lower, "not permitted") || strings.Contains(lower, "denied") || strings.Contains(lower, "deny file-write") || strings.Contains(lower, "error:") {
			picked = line
			break
		}
	}
	if picked == "" && class != "" {
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line != "" {
				picked = line
				break
			}
		}
	}
	if len(picked) > 400 {
		picked = picked[:400]
	}
	return picked
}
