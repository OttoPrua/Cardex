package sched

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
	"github.com/OttoPrua/Cardex/internal/store"
)

const (
	repairCanary = "CANARY-SENTINEL-9f3a"
	repairSecret = "SUPERSECRET"
)

func TestRepairBriefReachesFakeExecutor(t *testing.T) {
	ctx := context.Background()
	home, db, repo := newHome(t)
	_, obj := seedRepo(t, db, repo, "")
	sandboxOffProject(t, db)
	seedRoutes(t, db)
	branch(t, repo, obj, "c1", "a.txt", "changed\n")
	card := model.Card{
		ID: "c1", Objective: obj, Kind: model.KindDev, Title: "c1", Tier: model.TierT2,
		Status: model.CardMergeQueued, Channel: "cha", Base: "main",
		Acceptance: []string{repairFailCommand()},
	}
	if err := db.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	r := New(home)
	r.Script = func(model.Card) ([]byte, bool) {
		return []byte(`{"attempts":{"0":{"exit_code":0,"output":"ack"}}}`), true
	}
	trackLaunch(t, r)
	if err := r.Tick(ctx); err != nil {
		t.Fatal(readLog(home, err))
	}
	repair, err := db.GetCard(ctx, "rp-c1")
	if err != nil {
		t.Fatal(err)
	}
	if repair.Kind != model.KindRepair || repair.Channel != "cha" || repair.Body != repair.ReworkNote {
		t.Fatalf("repair channel %s kind %s", repair.Channel, repair.Kind)
	}
	if !strings.Contains(repair.Body, repairCanary) || !strings.Contains(repair.Body, "token="+repairSecret) || len(repair.Body) < 1<<20 {
		t.Fatalf("stored archive len %d", len(repair.Body))
	}
	prompt := promptReceived(t, home, db, repair.ID)
	assertSafePrompt(t, prompt)
	t.Logf("synthetic repair archive: %d bytes stored; model receives %d-byte card summary in a %d-byte prompt",
		len(repair.Body), len(promptCardBody(prompt)), len(prompt))
	if strings.Contains(joinedAttemptText(t, home, db, repair.ID), repairCanary) || strings.Contains(joinedAttemptText(t, home, db, repair.ID), repairSecret) {
		t.Fatal("executor log or launcher carried the archive")
	}
	events, err := db.ListEvents(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if strings.Contains(string(event.Data), repairCanary) || strings.Contains(string(event.Data), repairSecret) {
			t.Fatalf("event %s leaked the archive", event.Type)
		}
	}
}

func TestStoredRepairBriefIsFoldedOnLaunch(t *testing.T) {
	ctx := context.Background()
	home, db, repo := newHome(t)
	_, obj := seedRepo(t, db, repo, "")
	sandboxOffProject(t, db)
	seedRoutes(t, db)
	branch(t, repo, obj, "old", "a.txt", "old\n")
	body := repairArchive()
	card := model.Card{
		ID: "old", Objective: obj, Kind: model.KindRepair, Title: "old", Tier: model.TierT2,
		Status: model.CardReady, Base: "cardex/" + obj + "/c/old", Body: body, ReworkNote: body,
	}
	if err := db.CreateCard(ctx, &card); err != nil {
		t.Fatal(err)
	}
	r := New(home)
	r.Script = func(model.Card) ([]byte, bool) {
		return []byte(`{"attempts":{"0":{"exit_code":0,"output":"ack"}}}`), true
	}
	trackLaunch(t, r)
	if err := r.Tick(ctx); err != nil {
		t.Fatal(readLog(home, err))
	}
	prompt := promptReceived(t, home, db, "old")
	assertSafePrompt(t, prompt)
	stored, err := db.GetCard(ctx, "old")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Body != body || stored.ReworkNote != body {
		t.Fatal("stored archive was rewritten")
	}
}

func TestNoOutputFallbackLeavesStoredChannel(t *testing.T) {
	ctx := context.Background()
	home, db, repo := newHome(t)
	_, obj := seedRepo(t, db, repo, "")
	sandboxOffProject(t, db)
	seedRoutes(t, db)
	plain := model.Card{
		ID: "plain", Objective: obj, Kind: model.KindDev, Title: "plain", Tier: model.TierT2,
		Status: model.CardReady, Base: "main",
	}
	pinned := model.Card{
		ID: "pinned", Objective: obj, Kind: model.KindDev, Title: "pinned", Tier: model.TierT2,
		Status: model.CardReady, Base: "main", Channel: "cha",
	}
	if err := db.CreateCard(ctx, &plain); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateCard(ctx, &pinned); err != nil {
		t.Fatal(err)
	}
	r := New(home)
	r.Script = func(model.Card) ([]byte, bool) {
		return []byte(`{"attempts":{"0":{"exit_code":0,"output":"ack"}}}`), true
	}
	trackLaunch(t, r)
	deadline := time.Now().Add(20 * time.Second)
	var plainCard, pinnedCard model.Card
	for time.Now().Before(deadline) {
		if err := r.Tick(ctx); err != nil {
			t.Fatal(readLog(home, err))
		}
		var err error
		plainCard, err = db.GetCard(ctx, "plain")
		if err != nil {
			t.Fatal(err)
		}
		pinnedCard, err = db.GetCard(ctx, "pinned")
		if err != nil {
			t.Fatal(err)
		}
		moved := func(card model.Card, id string) bool {
			return hasAutoRoute(t, db, id) && card.Channel == "chb" && (card.Status == model.CardReady || card.Status == model.CardRunning)
		}
		if moved(plainCard, "plain") && moved(pinnedCard, "pinned") {
			break
		}
		time.Sleep(40 * time.Millisecond)
	}
	if (plainCard.Status != model.CardReady && plainCard.Status != model.CardRunning) || plainCard.Channel != "chb" || !hasAutoRoute(t, db, "plain") {
		t.Fatalf("plain %+v", plainCard)
	}
	attempts, err := db.ListAttempts(ctx, "plain")
	var first *model.Attempt
	for i := range attempts {
		if attempts[i].Index == 0 {
			first = &attempts[i]
		}
	}
	if err != nil || first == nil || first.Route.Channel != "cha" || explicitScore(first.RouteReason) {
		t.Fatalf("plain attempts %+v %v", attempts, err)
	}
	questions, err := db.ListQuestions(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	var pinnedQ *model.Question
	for i := range questions {
		if questions[i].Scope == "pinned" {
			pinnedQ = &questions[i]
		}
	}
	if (pinnedCard.Status != model.CardReady && pinnedCard.Status != model.CardRunning) || pinnedCard.Channel != "chb" || !hasAutoRoute(t, db, "pinned") {
		t.Fatalf("pinned %+v", pinnedCard)
	}
	var pinnedFirst *model.Attempt
	pinnedAttempts, err := db.ListAttempts(ctx, "pinned")
	if err != nil {
		t.Fatal(err)
	}
	for i := range pinnedAttempts {
		if pinnedAttempts[i].Index == 0 {
			pinnedFirst = &pinnedAttempts[i]
		}
	}
	if pinnedFirst == nil || pinnedFirst.Route.Channel != "cha" || !explicitScore(pinnedFirst.RouteReason) {
		t.Fatalf("pinned first attempt %+v", pinnedAttempts)
	}
	if pinnedQ != nil && strings.HasPrefix(pinnedQ.Text, "no other channel is available") {
		t.Fatalf("stored channel blocked the move %+v", pinnedQ)
	}
}

func TestDerivedRepairCopiesUserChannel(t *testing.T) {
	ctx := context.Background()
	home, db, repo := newHome(t)
	_, obj := seedRepo(t, db, repo, "echo RED_TEST; exit 1")
	sandboxOffProject(t, db)
	seedRoutes(t, db)
	branch(t, repo, obj, "auto", "a.txt", "auto\n")
	branch(t, repo, obj, "objonly", "b.txt", "obj\n")
	raw, _ := json.Marshal(map[string]string{"channel": "cha", "pin": "chb"})
	if err := db.AppendEvent(ctx, &model.Event{Entity: "card:auto", Type: "auto_route", Data: raw}); err != nil {
		t.Fatal(err)
	}
	auto := model.Card{
		ID: "auto", Objective: obj, Kind: model.KindDev, Title: "auto", Tier: model.TierT2,
		Status: model.CardMergeQueued, Channel: "cha", Base: "main",
	}
	objOnly := model.Card{
		ID: "objonly", Objective: obj, Kind: model.KindDev, Title: "objonly", Tier: model.TierT2,
		Status: model.CardMergeQueued, Base: "main",
	}
	if err := db.CreateCard(ctx, &auto); err != nil || db.CreateCard(ctx, &objOnly) != nil {
		t.Fatal(err)
	}
	got, err := db.GetObjective(ctx, obj)
	if err != nil {
		t.Fatal(err)
	}
	got.Channel = "cha"
	if err := db.UpdateObjective(ctx, &got); err != nil {
		t.Fatal(err)
	}
	r := New(home)
	// Examine the card produced by the real merge queue before an executor
	// can immediately finish and request another ordinary route.
	for i := 0; i < 2; i++ {
		if err := r.mergeStep(ctx, db); err != nil {
			t.Fatal(readLog(home, err))
		}
	}
	repairAuto, err := db.GetCard(ctx, "rp-auto")
	if err != nil {
		t.Fatal(err)
	}
	repairObj, err := db.GetCard(ctx, "rp-objonly")
	if err != nil {
		t.Fatal(err)
	}
	if repairAuto.Channel != "chb" || !strings.Contains(repairAuto.Body, "RED_TEST") || !strings.Contains(repairAuto.ReworkNote, "RED_TEST") {
		t.Fatalf("auto repair channel %s", repairAuto.Channel)
	}
	if repairObj.Channel != "" || !strings.Contains(repairObj.Body, "RED_TEST") {
		t.Fatalf("objective repair channel %q", repairObj.Channel)
	}
}

func sandboxOffProject(t *testing.T, db *store.Store) {
	t.Helper()
	project, err := db.GetProject(context.Background(), "p")
	if err != nil {
		t.Fatal(err)
	}
	project.Sandbox = "off"
	if err := db.UpdateProject(context.Background(), &project); err != nil {
		t.Fatal(err)
	}
}

func repairArchive() string {
	var b strings.Builder
	b.WriteString("UNLABELED-SENTINEL-dd44\nsession_key: FIELD-SENTINEL-ff66\n")
	b.WriteString("file: a.txt\n<<<<<<<\ntoken=")
	b.WriteString(repairSecret)
	b.WriteString("\n")
	b.WriteString(strings.Repeat("M", 1<<20))
	b.WriteByte('\n')
	b.WriteString(repairCanary)
	b.WriteByte('\n')
	b.WriteString(strings.Repeat("Z", 2048))
	b.WriteString("\nexit: exit status 1\n")
	return b.String()
}

func repairFailCommand() string {
	// The command text is copied into the prompt. The secret and sentinel
	// exist only in the check output, not in this source line.
	return `perl -e 'print "file: a.txt\n"; print "<" x 7; print "\ntoken="; print "SUPER","SECRET","\n"; print "M" x 1048576; print "\n"; print "CANARY-SENTINEL-","9f3a","\n"; print "Z" x 2048; print "exit: exit status 1\n"; exit 1'`
}

func assertSafePrompt(t *testing.T, prompt string) {
	t.Helper()
	for _, leak := range []string{repairCanary, repairSecret, "UNLABELED-SENTINEL-dd44", "FIELD-SENTINEL-ff66", "返工意见", "…[truncated", "[redacted]"} {
		if strings.Contains(prompt, leak) {
			t.Fatalf("prompt sent %q", leak)
		}
	}
	body := promptCardBody(prompt)
	for _, leak := range []string{"file:", "<<<<<<<", "exit:", "session_key"} {
		if strings.Contains(body, leak) {
			t.Fatalf("card body sent %q\n%s", leak, body)
		}
	}
	if !strings.Contains(body, "错误类别：") || !strings.Contains(body, "计数：") || !strings.Contains(body, "check_failed") || !strings.Contains(body, "conflict") {
		t.Fatalf("prompt missing the safe summary\n%s", body)
	}
	if len(prompt) > 64*1024 || strings.Contains(prompt, strings.Repeat("M", 8000)) {
		t.Fatalf("prompt len %d is not bounded", len(prompt))
	}
}

func promptCardBody(prompt string) string {
	const start = "正文：\n"
	i := strings.Index(prompt, start)
	if i < 0 {
		return prompt
	}
	rest := prompt[i+len(start):]
	j := strings.Index(rest, "\n声明：")
	if j < 0 {
		return rest
	}
	return rest[:j]
}

func promptReceived(t *testing.T, home string, db *store.Store, cardID string) string {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		attempts, err := db.ListAttempts(context.Background(), cardID)
		if err != nil {
			t.Fatal(err)
		}
		for _, attempt := range attempts {
			if attempt.PromptPath == "" {
				continue
			}
			body, err := os.ReadFile(attempt.PromptPath)
			if err != nil {
				last = err.Error()
				continue
			}
			logPath := filepath.Join(home, "attempts", attempt.ID, "output.log")
			logBody, err := os.ReadFile(logPath)
			if err != nil {
				last = "waiting for executor output"
				continue
			}
			if !strings.Contains(string(logBody), "prompt="+attempt.PromptPath) {
				sample := string(logBody)
				if len(sample) > 400 {
					sample = sample[:400]
				}
				launch, _ := os.ReadFile(filepath.Join(home, "attempts", attempt.ID, "launcher.log"))
				launchSample := string(launch)
				if len(launchSample) > 400 {
					launchSample = launchSample[:400]
				}
				last = fmt.Sprintf("output %q launcher %q path %s", sample, launchSample, attempt.PromptPath)
				continue
			}
			return string(body)
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("executor did not receive a prompt: %s", last)
	return ""
}

func joinedAttemptText(t *testing.T, home string, db *store.Store, cardID string) string {
	t.Helper()
	attempts, err := db.ListAttempts(context.Background(), cardID)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, attempt := range attempts {
		for _, name := range []string{"output.log", "launcher.log"} {
			raw, err := os.ReadFile(filepath.Join(home, "attempts", attempt.ID, name))
			if err == nil {
				b.Write(raw)
			}
		}
	}
	return b.String()
}

func hasAutoRoute(t *testing.T, db *store.Store, cardID string) bool {
	t.Helper()
	events, err := db.ListEvents(context.Background(), "card:"+cardID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Type == "auto_route" {
			return true
		}
	}
	return false
}

func explicitScore(reason model.RouteReason) bool {
	return len(reason.Scores) == 1 && reason.Scores[0].Score == 0 && len(reason.Filtered) == 0
}
