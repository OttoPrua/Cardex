package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/OttoPrua/Cardex/internal/model"
)

const StrongBundleVersion = 1

// StrongBundle contains no internal lineage metadata. In the blind role there
// is no recipient seat or model in the bytes, and no upstream material.
type StrongBundle struct {
	Version     int                    `json:"version"`
	Instruction string                 `json:"instruction"`
	Task        string                 `json:"task"`
	Materials   []strongBundleMaterial `json:"materials,omitempty"`
}

type strongBundleMaterial struct {
	Reference  string   `json:"reference"`
	Body       string   `json:"body"`
	References []string `json:"references,omitempty"`
}

// AssembleStrongBundle resolves one pinned task by the approved digest, never
// a prior transcript or a recipient's state. Stage and source IDs remain in SQL.
func AssembleStrongBundle(ctx context.Context, db *Store, target model.StrongStage, approvedTaskSHA string) ([]byte, error) {
	if db == nil || !target.Valid() || target.Role == model.StrongEvidence {
		return nil, model.ErrStrongArtifact
	}
	var body []byte
	err := db.WithTx(ctx, func(ctx context.Context) error {
		var err error
		body, err = assembleStrongBundle(ctx, db, target, approvedTaskSHA)
		return err
	})
	if err != nil {
		return nil, err
	}
	return body, nil
}

func assembleStrongBundle(ctx context.Context, db *Store, target model.StrongStage, approvedTaskSHA string) ([]byte, error) {
	approvedTask, err := db.StrongTask(ctx, target.Run, approvedTaskSHA)
	if err != nil {
		return nil, err
	}
	in, err := db.StrongInputs(ctx, target)
	if err != nil {
		return nil, err
	}
	b := StrongBundle{Version: StrongBundleVersion, Task: string(approvedTask)}
	switch target.Role {
	case model.StrongBlind:
		b.Instruction = "Answer the task independently. No upstream answers are provided."
	case model.StrongPeer:
		b.Instruction = "Review only the supplied anonymous materials. Refer to them only by their supplied reference labels."
	case model.StrongSynthesis:
		b.Instruction = "Synthesize the supplied anonymous materials. Refer to them only by their supplied reference labels."
	case model.StrongRecheck:
		b.Instruction = "Recheck the supplied anonymous material against the task."
	default:
		return nil, model.ErrStrongArtifact
	}
	n := len(approvedTask)
	bodies := [][]byte{approvedTask}
	for _, m := range in {
		n += len(m.Body)
		if n > model.StrongBundleMax {
			return nil, errors.New("strong-thinking input bundle too large")
		}
		bodies = append(bodies, m.Body)
		b.Materials = append(b.Materials, strongBundleMaterial{Reference: m.Alias, Body: string(m.Body), References: m.References})
	}
	if err := db.CheckStrongAnonymity(ctx, target, approvedTaskSHA, bodies...); err != nil {
		return nil, err
	}
	body, err := json.Marshal(b)
	if err != nil {
		return nil, err
	}
	if len(body) > model.StrongBundleMax {
		return nil, errors.New("strong-thinking input bundle too large")
	}
	return body, nil
}
