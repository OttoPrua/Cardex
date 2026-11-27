package loop

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/store"
)

// StageParentEvent binds a listed implementation card to the precise plan or
// replan attempt that produced its task. It does not add a scheduling dependency.
const StageParentEvent = "stage_handoff_parent"

func appendStageParents(ctx context.Context, db *store.Store, cardIDs []string, source, attempt string) error {
	if attempt == "" || source == "" || len(cardIDs) == 0 {
		return nil
	}
	raw, err := json.Marshal(struct {
		Card    string `json:"card"`
		Attempt string `json:"attempt"`
	}{source, attempt})
	if err != nil {
		return err
	}
	for _, id := range cardIDs {
		if err := db.AppendEvent(ctx, &model.Event{Entity: "card:" + id, Type: StageParentEvent, Data: raw}); err != nil {
			return err
		}
	}
	return nil
}

// StageDesignSource resolves the currently frozen design proposal without
// treating a proposal from a superseded generation as the latest decision.
func StageDesignSource(ctx context.Context, db *store.Store, node model.DesignNode) (string, error) {
	if node.Status != model.NodeFrozen || strings.TrimSpace(node.ID) == "" {
		return "", nil
	}
	d := Deps{Store: db}
	notes, err := d.reopenNotes(ctx, node.ID)
	if err != nil {
		return "", err
	}
	return d.freezableForGen(ctx, node.ID, len(notes))
}

func (d Deps) linkListedStages(ctx context.Context, source string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	attemptID, err := d.currentAttemptID(ctx, source)
	if err != nil {
		return err
	}
	return appendStageParents(ctx, d.Store, ids, source, attemptID)
}
