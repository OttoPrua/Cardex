package loop

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"cardex/internal/model"
	"cardex/internal/positioning"
	"cardex/internal/prompt"
	"cardex/internal/scorecard"
	"cardex/internal/store"
)

const (
	sourcesStart      = "<!-- sources:start -->"
	sourcesEnd        = "<!-- sources:end -->"
	rankingSourcesKey = "ranking_sources"
	rankingApprove    = "批准"
	rankingMark       = "定位提案"
	week              = 168 * time.Hour
)

// ScheduleTask is one built-in timer. A home that does not set schedules uses this list.
type ScheduleTask struct {
	Name     string
	Every    time.Duration
	Kind     model.CardKind
	Requires []string
}

// DefaultSchedule is the weekly ranking task used when config.json has no schedules key.
// sched.schedules still returns when that key is absent; the production binding calls this.
func DefaultSchedule(home string) ([]ScheduleTask, error) {
	set, err := schedulesConfigured(home)
	if err != nil || set {
		return nil, err
	}
	return []ScheduleTask{{
		Name:     "ranking",
		Every:    week,
		Kind:     model.KindRanking,
		Requires: []string{"web"},
	}}, nil
}

// DefaultRankingCardID is the card id for the weekly ranking task at now.
// The slot matches a duration schedule: unix seconds divided by the week length.
func DefaultRankingCardID(now time.Time) string {
	secs := int64(week / time.Second)
	if secs < 1 {
		secs = 1
	}
	return "sch-ranking-" + strconv.FormatInt(now.Unix()/secs, 10)
}

// ApplyDefaultSchedule enqueues the weekly ranking card when config.json has no
// schedules key. The sched owner calls this from the schedule entry. Opening a
// database does not.
func ApplyDefaultSchedule(ctx context.Context, db *store.Store, home string, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if db == nil {
		return errNilStore
	}
	tasks, err := DefaultSchedule(home)
	if err != nil || len(tasks) == 0 {
		return err
	}
	objectiveID, err := activeObjective(ctx, db)
	if err != nil || objectiveID == "" {
		return err
	}
	return EnqueueRankingSchedule(ctx, db, objectiveID, tasks, now)
}

// EnqueueRankingSchedule stores one card per ranking schedule definition.
// Each card keeps that definition's full Requires list.
func EnqueueRankingSchedule(ctx context.Context, db *store.Store, objectiveID string, tasks []ScheduleTask, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if db == nil {
		return errNilStore
	}
	if strings.TrimSpace(objectiveID) == "" {
		return nil
	}
	for _, task := range tasks {
		if task.Kind != "" && task.Kind != model.KindRanking {
			continue
		}
		id := rankingScheduleCardID(task, now)
		if _, err := db.GetCard(ctx, id); err == nil {
			continue
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		card := ScheduleCard(objectiveID, id, task)
		if err := db.CreateCard(ctx, &card); err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				continue
			}
			return err
		}
	}
	return nil
}

// ScheduleCard builds the card a ranking timer stores for one schedule definition.
// Requires is a copy of that definition's list, including an explicit ["web"].
func ScheduleCard(objectiveID, cardID string, task ScheduleTask) model.Card {
	kind := task.Kind
	if kind == "" {
		kind = model.KindRanking
	}
	title := strings.TrimSpace(task.Name)
	if title == "" {
		title = string(kind)
	}
	return model.Card{
		ID:        cardID,
		Objective: objectiveID,
		Kind:      kind,
		Title:     title,
		Tier:      model.DefaultTier(kind),
		Status:    model.CardReady,
		Requires:  append([]string(nil), task.Requires...),
	}
}

func rankingScheduleCardID(task ScheduleTask, now time.Time) string {
	if task.Name == "" || task.Name == "ranking" {
		return DefaultRankingCardID(now)
	}
	every := task.Every
	if every <= 0 {
		every = week
	}
	secs := int64(every / time.Second)
	if secs < 1 {
		secs = 1
	}
	return "sch-" + task.Name + "-" + strconv.FormatInt(now.Unix()/secs, 10)
}

func activeObjective(ctx context.Context, db *store.Store) (string, error) {
	objs, err := db.ListObjectives(ctx, "")
	if err != nil {
		return "", err
	}
	for _, obj := range objs {
		if obj.Status == model.ObjectiveActive {
			return obj.ID, nil
		}
	}
	return "", nil
}

// RankingMaterial is the ranking prompt text. A ranking_sources list in config.json
// replaces the default source list. A non-empty home also appends the current
// positioning table, each channel's models, and the scorecard summary.
func RankingMaterial(home string) (string, error) {
	base, err := prompt.Preface(model.KindRanking)
	if err != nil {
		return "", err
	}
	sources, ok, err := configuredSources(home)
	if err != nil {
		return "", err
	}
	if ok {
		base = replaceSources(base, sources)
	}
	if strings.TrimSpace(home) == "" {
		return base, nil
	}
	snap, err := inputSnapshot(home)
	if err != nil {
		return "", err
	}
	if !strings.HasSuffix(base, "\n") {
		base += "\n"
	}
	return base + "\n" + snap, nil
}

func inputSnapshot(home string) (string, error) {
	ctx := context.Background()
	var b strings.Builder
	rows, err := currentPositioning(ctx, home)
	if err != nil {
		return "", err
	}
	writePositioning(&b, rows)
	channels, err := channelModels(ctx, home)
	if err != nil {
		return "", err
	}
	writeChannels(&b, channels)
	summary, err := scoreSummary(ctx, home)
	if err != nil {
		return "", err
	}
	writeScorecard(&b, summary)
	return b.String(), nil
}

func currentPositioning(ctx context.Context, home string) ([]model.Positioning, error) {
	if !homeDBExists(home) {
		return nil, nil
	}
	table, err := positioning.Open(home)
	if err != nil {
		return nil, err
	}
	defer table.Close()
	rows, err := table.Current(ctx)
	if err != nil {
		return nil, err
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Model < rows[j].Model })
	return rows, nil
}

func channelModels(ctx context.Context, home string) ([]model.Channel, error) {
	if !homeDBExists(home) {
		return nil, nil
	}
	db, err := store.Open(filepath.Join(home, "cardex.db"))
	if err != nil {
		return nil, err
	}
	defer db.Close()
	channels, err := db.ListChannels(ctx)
	if err != nil {
		return nil, err
	}
	sort.Slice(channels, func(i, j int) bool { return channels[i].ID < channels[j].ID })
	return channels, nil
}

func scoreSummary(ctx context.Context, home string) ([]scorecard.Row, error) {
	if !homeDBExists(home) {
		return nil, nil
	}
	return scorecard.Compute(ctx, home)
}

func homeDBExists(home string) bool {
	info, err := os.Stat(filepath.Join(home, "cardex.db"))
	return err == nil && !info.IsDir()
}

func writePositioning(b *strings.Builder, rows []model.Positioning) {
	b.WriteString("## 当前定位表\n")
	if len(rows) == 0 {
		b.WriteString("（无）\n")
		return
	}
	for _, row := range rows {
		b.WriteString("- model=")
		b.WriteString(row.Model)
		b.WriteString(" family=")
		b.WriteString(row.Family)
		b.WriteString(" tiers=")
		b.WriteString(joinTiers(row.Tiers))
		b.WriteString(" capabilities=")
		b.WriteString(strings.Join(row.Capabilities, ","))
		b.WriteString(" preference=")
		b.WriteString(strconv.Itoa(row.Preference))
		b.WriteByte('\n')
	}
}

func writeChannels(b *strings.Builder, channels []model.Channel) {
	b.WriteString("## 各渠道可用的模型清单\n")
	if len(channels) == 0 {
		b.WriteString("（无）\n")
		return
	}
	for _, ch := range channels {
		b.WriteString("- channel=")
		b.WriteString(ch.ID)
		b.WriteString(" models=")
		b.WriteString(strings.Join(ch.Models, ","))
		b.WriteByte('\n')
	}
}

func writeScorecard(b *strings.Builder, rows []scorecard.Row) {
	b.WriteString("## 成绩单摘要\n")
	if len(rows) == 0 {
		b.WriteString("（无）\n")
		return
	}
	for _, row := range rows {
		b.WriteString("- model=")
		b.WriteString(row.Model)
		b.WriteString(" kind=")
		b.WriteString(string(row.Kind))
		b.WriteString(" tier=")
		b.WriteString(string(row.Tier))
		b.WriteString(" 复核通过率=")
		b.WriteString(formatFigure(row.ReviewPassRate))
		b.WriteString(" 合入率=")
		b.WriteString(formatFigure(row.MergeRate))
		b.WriteString(" 平均返工=")
		b.WriteString(formatFigure(row.AvgRework))
		b.WriteString(" 每张合入token=")
		b.WriteString(formatFigure(row.TokensPerMerge))
		b.WriteString(" 样本=")
		b.WriteString(strconv.Itoa(row.Sample))
		if row.Insufficient {
			b.WriteString(" 样本不足")
		}
		b.WriteByte('\n')
	}
}

func joinTiers(tiers []model.Tier) string {
	parts := make([]string, len(tiers))
	for i, tier := range tiers {
		parts[i] = string(tier)
	}
	return strings.Join(parts, ",")
}

func formatFigure(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

func schedulesConfigured(home string) (bool, error) {
	top, ok, err := readConfig(home)
	if err != nil || !ok {
		return false, err
	}
	_, set := top["schedules"]
	return set, nil
}

func configuredSources(home string) ([]string, bool, error) {
	top, ok, err := readConfig(home)
	if err != nil || !ok {
		return nil, false, err
	}
	raw, exists := top[rankingSourcesKey]
	if !exists {
		return nil, false, nil
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, false, nil
	}
	return list, true, nil
}

func readConfig(home string) (map[string]json.RawMessage, bool, error) {
	if strings.TrimSpace(home) == "" {
		return nil, false, nil
	}
	body, err := os.ReadFile(filepath.Join(home, "config.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return nil, false, nil
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		return nil, false, nil
	}
	return top, true, nil
}

func replaceSources(text string, sources []string) string {
	start := strings.Index(text, sourcesStart)
	end := strings.Index(text, sourcesEnd)
	if start < 0 || end < 0 || end < start {
		return text
	}
	var b strings.Builder
	b.WriteString(text[:start+len(sourcesStart)])
	b.WriteByte('\n')
	for _, source := range sources {
		b.WriteString("- ")
		b.WriteString(source)
		b.WriteByte('\n')
	}
	b.WriteString(text[end:])
	return b.String()
}

// FollowRanking publishes a proposal after its question is answered with 批准.
// Stored attempt routes are left as they were.
func FollowRanking(ctx context.Context, db *store.Store) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if db == nil {
		return errNilStore
	}
	home := currentRankingHome()
	questions, err := db.ListQuestions(ctx, "")
	if err != nil {
		return err
	}
	var approved []string
	for _, q := range questions {
		if strings.TrimSpace(q.Answer) != rankingApprove {
			continue
		}
		id := proposalID(q.Text)
		if id == "" {
			continue
		}
		status, err := proposalStatus(home, id)
		if err != nil {
			return err
		}
		if status != "" && status != "proposed" {
			continue
		}
		approved = append(approved, id)
	}
	if len(approved) == 0 {
		return nil
	}
	table, err := positioning.Open(home)
	if err != nil {
		return err
	}
	defer table.Close()
	for _, id := range approved {
		if err := table.Approve(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

// RankingHarvest is one harvested ranking attempt. deliver passes it after harvest.
// absorbAttempt passes it for a ranking card. Opening a database does not.
type RankingHarvest struct {
	AttemptID  string
	Outcome    model.Outcome
	ErrorClass model.ErrorClass
	Acceptance string // "failed" is 验收失败. Empty or "passed" is not.
	ResultText string
}

// AbsorbHarvestedRanking absorbs a ranking result only when the harvest passed.
// violation, budget_limited, 验收失败, and a stopped card do not create a proposal
// and do not mark the card done. A pass stores the attempt outcome, so a later
// call does not absorb that same attempt again.
func AbsorbHarvestedRanking(ctx context.Context, d Deps, card model.Card, harvest RankingHarvest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if card.Kind != model.KindRanking {
		return nil
	}
	if d.Store == nil {
		return errNilStore
	}
	if card.ID != "" {
		fresh, err := d.Store.GetCard(ctx, card.ID)
		if err != nil {
			return err
		}
		if fresh.Status == model.CardFailed || fresh.Status == model.CardCanceled {
			card.Status = fresh.Status
		}
		if card.Objective == "" {
			card.Objective = fresh.Objective
		}
	}
	if harvest.AttemptID != "" {
		attempt, err := d.Store.GetAttempt(ctx, harvest.AttemptID)
		if err != nil {
			return err
		}
		if attempt.Outcome != "" {
			return nil
		}
	}
	if !rankingAbsorbable(card, harvest) {
		return nil
	}
	if err := d.absorbRanking(ctx, card, harvest.ResultText); err != nil {
		return err
	}
	if harvest.AttemptID == "" {
		return nil
	}
	attempt, err := d.Store.GetAttempt(ctx, harvest.AttemptID)
	if err != nil {
		return err
	}
	attempt.Outcome = harvest.Outcome
	if harvest.ErrorClass != "" {
		attempt.ErrorClass = harvest.ErrorClass
	}
	return d.Store.UpdateAttempt(ctx, &attempt)
}

// rankingAbsorbable is the harvest decision. Store writes stay in AbsorbHarvestedRanking.
func rankingAbsorbable(card model.Card, harvest RankingHarvest) bool {
	if card.Kind != model.KindRanking {
		return false
	}
	if card.Status == model.CardFailed || card.Status == model.CardCanceled {
		return false
	}
	if harvest.Outcome != model.OutcomePassed {
		return false
	}
	if harvest.ErrorClass != "" && harvest.ErrorClass != model.ErrorPassed {
		return false
	}
	switch strings.TrimSpace(harvest.Acceptance) {
	case "", "passed":
		return true
	default:
		return false
	}
}

// AbsorbRanking turns a ranking result into a positioning proposal and a question.
// Harvest gating lives in AbsorbHarvestedRanking. absorbAttempt does not dispatch
// ranking; the loop owner calls AbsorbHarvestedRanking from that dispatch.
func AbsorbRanking(ctx context.Context, d Deps, card model.Card, text string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if card.Kind != model.KindRanking {
		return nil
	}
	return d.absorbRanking(ctx, card, text)
}

func currentRankingHome() string {
	return strings.TrimSpace(os.Getenv("CARDEX_HOME"))
}

func (d Deps) absorbRanking(ctx context.Context, card model.Card, text string) error {
	if d.Store == nil {
		return errNilStore
	}
	questions, err := d.Store.ListQuestions(ctx, card.Objective)
	if err != nil {
		return err
	}
	for _, q := range questions {
		if q.Scope == card.ID && strings.Contains(q.Text, rankingMark) {
			return d.finishRanking(ctx, card.ID, model.CardDone, "")
		}
	}
	parsed := parseProposal(text)
	var prop positioning.Proposal
	var current []model.Positioning
	if len(parsed.entries) > 0 {
		home := currentRankingHome()
		table, err := positioning.Open(home)
		if err != nil {
			return err
		}
		current, err = table.Current(ctx)
		if err != nil {
			table.Close()
			return err
		}
		prop, err = table.Propose(ctx, parsed.entries)
		table.Close()
		if err != nil {
			return err
		}
	}
	body := rankingQuestion(prop, current, parsed)
	if _, err := d.questions().Create(ctx, model.Question{
		Objective: card.Objective,
		Scope:     card.ID,
		ScopeKind: "card",
		Kind:      model.QuestionCustom,
		Text:      body,
		Options:   []string{rankingApprove},
	}); err != nil {
		return err
	}
	status := model.CardDone
	reason := ""
	if len(parsed.entries) == 0 {
		status = model.CardNeedsInput
		reason = "待复核"
	}
	return d.finishRanking(ctx, card.ID, status, reason)
}

func (d Deps) finishRanking(ctx context.Context, cardID string, status model.CardStatus, reason string) error {
	fresh, err := d.Store.GetCard(ctx, cardID)
	if err != nil {
		return err
	}
	fresh.Status = status
	fresh.WaitReason = reason
	return d.Store.UpdateCard(ctx, &fresh)
}

type parsedProposal struct {
	entries []model.Positioning
	reasons map[string]string
	notes   []string
	flagged []string
}

func parseProposal(text string) parsedProposal {
	out := parsedProposal{reasons: map[string]string{}}
	values, broken := scanJSON(text)
	if broken > 0 {
		out.notes = append(out.notes, "待复核")
	}
	if len(values) == 0 && strings.TrimSpace(text) != "" {
		out.notes = appendUnique(out.notes, "待复核")
	}
	for _, raw := range values {
		entries, reasons, notes, flagged := entriesFrom(raw)
		out.entries = append(out.entries, entries...)
		for modelName, reason := range reasons {
			if _, ok := out.reasons[modelName]; !ok {
				out.reasons[modelName] = reason
			}
		}
		out.notes = append(out.notes, notes...)
		out.flagged = append(out.flagged, flagged...)
	}
	if len(out.notes) > 1 {
		out.notes = []string{"待复核"}
	}
	return out
}

func entriesFrom(raw json.RawMessage) ([]model.Positioning, map[string]string, []string, []string) {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 {
		return nil, nil, []string{"待复核"}, nil
	}
	if raw[0] == '[' {
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return nil, nil, []string{"待复核"}, nil
		}
		var entries []model.Positioning
		reasons := map[string]string{}
		var notes, flagged []string
		for _, item := range items {
			entry, reason, note, ok := oneEntry(item)
			if !ok {
				notes = appendUnique(notes, "待复核")
				continue
			}
			if note != "" {
				notes = appendUnique(notes, note)
			}
			entries = append(entries, entry)
			if reason != "" {
				reasons[entry.Model] = reason
			}
		}
		return entries, reasons, notes, flagged
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, nil, []string{"待复核"}, nil
	}
	var flagged []string
	if rawNew, ok := obj["新出现"]; ok {
		flagged = append(flagged, stringList(rawNew)...)
	}
	if ents, ok := obj["entries"]; ok {
		entries, reasons, notes, nested := entriesFrom(ents)
		return entries, reasons, notes, append(flagged, nested...)
	}
	entry, reason, note, ok := oneEntry(raw)
	if !ok {
		return nil, nil, []string{"待复核"}, flagged
	}
	var notes []string
	if note != "" {
		notes = append(notes, note)
	}
	reasons := map[string]string{}
	if reason != "" {
		reasons[entry.Model] = reason
	}
	return []model.Positioning{entry}, reasons, notes, flagged
}

func oneEntry(raw json.RawMessage) (model.Positioning, string, string, bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return model.Positioning{}, "", "", false
	}
	entry := model.Positioning{}
	note := ""
	if rawModel, ok := fields["model"]; ok {
		name, ok := stringField(rawModel)
		if !ok {
			return model.Positioning{}, "", "待复核", false
		}
		entry.Model = name
	}
	if entry.Model == "" {
		return model.Positioning{}, "", "待复核", false
	}
	if rawFamily, ok := fields["family"]; ok {
		name, ok := stringField(rawFamily)
		if !ok {
			note = "待复核"
		} else {
			entry.Family = name
		}
	}
	if rawTiers, ok := fields["tiers"]; ok {
		tiers, valid := tierField(rawTiers)
		if !valid {
			note = "待复核"
		}
		entry.Tiers = tiers
	}
	if entry.Tiers == nil {
		entry.Tiers = []model.Tier{}
	}
	if rawCaps, ok := fields["capabilities"]; ok {
		caps, valid := stringListField(rawCaps)
		if !valid {
			note = "待复核"
		}
		entry.Capabilities = caps
	}
	if entry.Capabilities == nil {
		entry.Capabilities = []string{}
	}
	if rawPref, ok := fields["preference"]; ok {
		n, ok := intField(rawPref)
		if !ok {
			note = "待复核"
		} else {
			entry.Preference = n
		}
	}
	reason := ""
	if rawReason, ok := fields["理由"]; ok {
		text, ok := stringField(rawReason)
		if !ok {
			note = "待复核"
		} else {
			reason = text
		}
	} else if rawReason, ok := fields["reason"]; ok {
		text, ok := stringField(rawReason)
		if !ok {
			note = "待复核"
		} else {
			reason = text
		}
	}
	return entry, reason, note, true
}

func rankingQuestion(prop positioning.Proposal, current []model.Positioning, parsed parsedProposal) string {
	var b strings.Builder
	b.WriteString(rankingMark)
	if prop.ID != "" {
		b.WriteByte(' ')
		b.WriteString(prop.ID)
	}
	b.WriteByte('\n')
	b.WriteString("差异：\n")
	b.WriteString(prop.Diff)
	if prop.Diff != "" && !strings.HasSuffix(prop.Diff, "\n") {
		b.WriteByte('\n')
	}
	known := map[string]bool{}
	for _, row := range current {
		if row.Model != "" {
			known[row.Model] = true
		}
	}
	seen := map[string]bool{}
	var fresh []string
	for _, row := range parsed.entries {
		if row.Model == "" || known[row.Model] || seen[row.Model] {
			continue
		}
		seen[row.Model] = true
		fresh = append(fresh, row.Model)
	}
	for _, name := range parsed.flagged {
		if name == "" || known[name] || seen[name] {
			continue
		}
		seen[name] = true
		fresh = append(fresh, name)
	}
	b.WriteString("新出现：")
	if len(fresh) == 0 {
		b.WriteString("无")
	} else {
		b.WriteString(strings.Join(fresh, "、"))
	}
	b.WriteByte('\n')
	for _, row := range parsed.entries {
		reason := parsed.reasons[row.Model]
		if reason == "" {
			continue
		}
		b.WriteString("理由：")
		b.WriteString(row.Model)
		b.WriteString("：")
		b.WriteString(reason)
		b.WriteByte('\n')
	}
	if len(parsed.notes) > 0 {
		b.WriteString("待复核\n")
	}
	return b.String()
}

func proposalID(text string) string {
	line := ""
	for _, item := range strings.Split(text, "\n") {
		if strings.HasPrefix(item, rankingMark+" ") {
			line = item
			break
		}
	}
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return ""
	}
	return fields[1]
}

func proposalStatus(home, id string) (string, error) {
	if id == "" || strings.Contains(id, "/") || strings.Contains(id, "..") {
		return "", os.ErrInvalid
	}
	body, err := os.ReadFile(filepath.Join(home, "positioning", "proposals", id+".json"))
	if err != nil {
		return "", err
	}
	var saved struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &saved); err != nil {
		return "", err
	}
	return saved.Status, nil
}

func scanJSON(text string) ([]json.RawMessage, int) {
	b := []byte(text)
	var out []json.RawMessage
	broken := 0
	for i := 0; i < len(b); {
		if b[i] != '{' && b[i] != '[' {
			i++
			continue
		}
		end, ok := matchJSON(b, i)
		if !ok {
			broken++
			i++
			continue
		}
		frag := append([]byte(nil), b[i:end]...)
		if !json.Valid(frag) {
			broken++
			i++
			continue
		}
		out = append(out, json.RawMessage(frag))
		i = end
	}
	return out, broken
}

func matchJSON(b []byte, start int) (int, bool) {
	depth := 0
	inStr := false
	esc := false
	for i := start; i < len(b); i++ {
		c := b[i]
		if inStr {
			if esc {
				esc = false
				continue
			}
			if c == '\\' {
				esc = true
				continue
			}
			if c == '"' {
				inStr = false
			}
			continue
		}
		if c == '"' {
			inStr = true
			continue
		}
		if c == '{' || c == '[' {
			depth++
			continue
		}
		if c == '}' || c == ']' {
			depth--
			if depth == 0 {
				return i + 1, true
			}
			if depth < 0 {
				return 0, false
			}
		}
	}
	return 0, false
}

func stringField(raw json.RawMessage) (string, bool) {
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return "", false
	}
	return strings.TrimSpace(text), true
}

func intField(raw json.RawMessage) (int, bool) {
	var n int
	if err := json.Unmarshal(raw, &n); err == nil {
		return n, true
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err != nil {
		return 0, false
	}
	return int(f), true
}

func tierField(raw json.RawMessage) ([]model.Tier, bool) {
	items, ok := stringListField(raw)
	if !ok {
		return nil, false
	}
	valid := true
	out := make([]model.Tier, 0, len(items))
	for _, item := range items {
		tier := model.Tier(item)
		out = append(out, tier)
		if _, canon := model.CanonicalTier(tier); !canon {
			valid = false
		}
	}
	return out, valid
}

func stringListField(raw json.RawMessage) ([]string, bool) {
	if list := stringList(raw); list != nil {
		return list, true
	}
	var one string
	if err := json.Unmarshal(raw, &one); err != nil {
		return nil, false
	}
	one = strings.TrimSpace(one)
	if one == "" {
		return []string{}, true
	}
	return []string{one}, true
}

func stringList(raw json.RawMessage) []string {
	var items []string
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}

func appendUnique(items []string, item string) []string {
	for _, have := range items {
		if have == item {
			return items
		}
	}
	return append(items, item)
}

var errNilStore = errors.New("loop: nil store")
