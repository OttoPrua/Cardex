package store

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
)

func TestAPIChannelRawRefusalHonorsTypedCustodyWithoutCreatingSchema(t *testing.T) {
	for _, scenario := range []struct {
		name, ddl, row   string
		block, malformed bool
	}{
		{name: "ordinary-home"},
		{name: "pending-producer", row: `INSERT INTO strong_producers (attempt_id,card_id,worktree,run_id,stage_id,role,seat,token) VALUES ('a','c','safe','run','stage','reviewer','seat','token')`, block: true},
		{name: "foreign-producer", row: `INSERT INTO strong_producers (attempt_id,card_id,worktree,run_id,stage_id,role,seat,token) VALUES ('other','c','safe','run','stage','reviewer','seat','token')`},
		{name: "reserved-run-intent", ddl: strongWorkflowSchemaForTest, row: `INSERT INTO strong_run_intents (run_id,revision,slot,intent_id,attempt_id,card_id,state,route_json,reserved) VALUES ('run',1,'slot','intent','a','c','reserved','{}',1)`, block: true},
		{name: "foreign-run-intent", ddl: strongWorkflowSchemaForTest, row: `INSERT INTO strong_run_intents (run_id,revision,slot,intent_id,attempt_id,card_id,state,route_json,reserved) VALUES ('run',1,'slot','intent','other','c','reserved','{}',1)`},
		{name: "malformed-intent-table", ddl: "CREATE TABLE strong_run_intents (bogus TEXT)", block: true, malformed: true},
		{name: "invalid-intent-view", ddl: "CREATE VIEW strong_run_intents AS SELECT 'a' AS attempt_id", block: true, malformed: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx := t.Context()
			db := openDB(t)
			if scenario.ddl == strongWorkflowSchemaForTest {
				if err := db.InstallStrongWorkflowPrototype(ctx); err != nil {
					t.Fatal(err)
				}
			} else if scenario.ddl != "" {
				if _, err := db.db.ExecContext(ctx, scenario.ddl); err != nil {
					t.Fatal(err)
				}
			}
			if scenario.row != "" {
				if _, err := db.db.ExecContext(ctx, scenario.row); err != nil {
					t.Fatal(err)
				}
			}
			obj := model.Objective{ID: "o", Status: model.ObjectiveActive, Mode: model.ModeManual, FreezeMode: model.FreezeSafe}
			ch := model.Channel{ID: "raw", Runner: model.RunnerClaude, Health: model.HealthOK, EngineProfile: "profile"}
			c := model.Card{ID: "c", Objective: "o", Kind: model.KindDev, Status: model.CardRunning}
			for _, err := range []error{db.CreateObjective(ctx, &obj), db.CreateChannel(ctx, &ch), db.CreateCard(ctx, &c)} {
				if err != nil {
					t.Fatal(err)
				}
			}
			a := model.Attempt{ID: "a", Card: "c", Route: model.Route{Channel: ch.ID, Model: "fixture"}, StartedAt: time.Now().UTC()}
			if err := db.CreateAttempt(ctx, &a); err != nil {
				t.Fatal(err)
			}
			if err := db.RecordAttemptPreparation(ctx, a); err != nil {
				t.Fatal(err)
			}
			a, _ = db.GetAttempt(ctx, a.ID)
			c, _ = db.GetCard(ctx, c.ID)
			obj, _ = db.GetObjective(ctx, obj.ID)
			err := db.HoldCompatibleRefusal(ctx, c, a, &obj, time.Now(), CompatibleRefusal{
				ProfileDigest: strings.Repeat("a", 64), Verify: func(ch model.Channel) bool { return ch.ID == "raw" },
			})
			if (err != nil) != scenario.malformed {
				t.Fatalf("refusal error: %v", err)
			}
			gotA, err := db.GetAttempt(ctx, a.ID)
			if err != nil {
				t.Fatal(err)
			}
			gotC, err := db.GetCard(ctx, c.ID)
			if err != nil {
				t.Fatal(err)
			}
			events, err := db.ListEvents(ctx, "attempt:"+a.ID)
			if err != nil {
				t.Fatal(err)
			}
			if scenario.block {
				if !reflect.DeepEqual(gotA, a) || !reflect.DeepEqual(gotC, c) || len(events) != 1 || events[0].Type != model.EventAttemptPrepared {
					t.Fatalf("owned or malformed attempt was changed: %+v %+v %+v", gotA, gotC, events)
				}
			} else if gotA.Outcome != model.OutcomeNoOutput || gotC.Status != model.CardNeedsInput || len(events) != 3 {
				t.Fatalf("unowned attempt failed to record refusal: %+v %+v %+v", gotA, gotC, events)
			}
			if scenario.ddl == "" {
				var installed int
				if err := db.db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE name='strong_run_intents'`).Scan(&installed); err != nil || installed != 0 {
					t.Fatalf("refusal installed optional schema: %d %v", installed, err)
				}
			}
		})
	}
}

const strongWorkflowSchemaForTest = "fixture: optional custody v16"
