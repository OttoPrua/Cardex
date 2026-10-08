package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestProjectAPIShowsWaitThenRecovery(t *testing.T) {
	root := testRoot(t)
	if err := saveConfig(root, defaultConfig("claude")); err != nil {
		t.Fatal(err)
	}
	saveWaitTask(t, root, "t-pred-held", "held predecessor", statusHeld, "wait-alpha", nil)
	saveWaitTask(t, root, "t-child", "queued dependent", statusQueued, "wait-alpha", []string{"t-pred-held"})

	srv := newBoardServer(root, 0)
	get := func() map[string]any {
		t.Helper()
		snap, err := buildSnapshot(root, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		projID := ""
		for _, p := range snap.Projects {
			for _, ph := range p.Phases {
				for _, tb := range ph.Tasks {
					if tb.ID == "t-child" {
						projID = p.ID
					}
				}
			}
		}
		if projID == "" {
			t.Fatal("project id for dependent not found")
		}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/project?id="+projID, nil)
		srv.handleProject(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/project = %d %s", rec.Code, rec.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body
	}

	before := get()
	rawBefore, _ := json.Marshal(before)
	dumpScratch(t, "board-wait-before.json", string(rawBefore))
	if !strings.Contains(string(rawBefore), `"id":"t-pred-held"`) || !strings.Contains(string(rawBefore), depWaitHeld) {
		t.Fatalf("before JSON missing who/why: %s", rawBefore)
	}
	if !strings.Contains(string(rawBefore), `"waiting_on"`) {
		t.Fatalf("before JSON missing waiting_on: %s", rawBefore)
	}

	pred, err := loadTask(root, "t-pred-held")
	if err != nil {
		t.Fatal(err)
	}
	pred.Status = statusDone
	if err := writeTaskFile(root, pred); err != nil {
		t.Fatal(err)
	}
	after := get()
	rawAfter, _ := json.Marshal(after)
	dumpScratch(t, "board-wait-after.json", string(rawAfter))
	child, err := loadTask(root, "t-child")
	if err != nil {
		t.Fatal(err)
	}
	if child.Status != statusQueued {
		t.Fatalf("dependent status=%s", child.Status)
	}
	if strings.Contains(string(rawAfter), `"why":"held"`) && strings.Contains(string(rawAfter), `"id":"t-pred-held"`) {
		// held why on the recovered predecessor itself is fine; the child's waiting_on must be gone.
	}
	childJSON := ""
	// Walk columns for the queued dependent.
	enc, _ := json.Marshal(after)
	if !strings.Contains(string(enc), `"id":"t-child"`) {
		t.Fatal("dependent missing after recovery")
	}
	_ = childJSON
	if strings.Contains(string(rawAfter), `"waiting_on":[{"id":"t-pred-held"`) {
		t.Fatalf("stale waiting_on after recovery: %s", rawAfter)
	}
}

func TestAppJSConsumesWaitingOn(t *testing.T) {
	code := appJSCode(t)
	for _, needle := range []string{"waiting_on", "waitChips", "dependency wait"} {
		if !strings.Contains(code, needle) {
			t.Fatalf("web/app.js does not consume %q", needle)
		}
	}
}
