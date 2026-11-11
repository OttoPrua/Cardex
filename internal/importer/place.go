package importer

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/OttoPrua/Cardex/internal/model"
)

// importObjectiveTitle is the single manual objective created per v1 project.
const importObjectiveTitle = "v1 导入"

// v1Record is the subset of a v1 task or workflow JSON object this importer reads.
type v1Record struct {
	ID          string          `json:"id"`
	Title       string          `json:"title"`
	Goal        looseString     `json:"goal"`
	Type        string          `json:"type"`
	Status      string          `json:"status"`
	Project     string          `json:"project"`
	ModuleID    string          `json:"module_id"`
	Dir         string          `json:"dir"`
	Repo        string          `json:"repo"`
	HoldReason  string          `json:"hold_reason"`
	Reason      string          `json:"reason"`
	LastError   string          `json:"last_error"`
	RunnerPref  string          `json:"runner_pref"`
	FrozenRoute json.RawMessage `json:"frozen_route"`
	Model       string          `json:"model"`
	Prompts     []string        `json:"prompts"`
	Step        int             `json:"step"`
	DependsOn   []string        `json:"depends_on"`
}

// looseString keeps a JSON string. A v1 task goal is often an object
// (the native-goal binding), which is not a title. Decoding that object
// as empty leaves the card in place so status still decides the v2 row.
type looseString string

func (s *looseString) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		*s = ""
		return nil
	}
	if data[0] != '"' {
		*s = ""
		return nil
	}
	var v string
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	*s = looseString(v)
	return nil
}

// placement is the v2 row a parsed record becomes. It does not touch the disk.
type placement struct {
	ProjectKey string
	Title      string
	Kind       model.CardKind
	RepoPath   string
	Body       string
	Deps       []string
	Status     model.CardStatus
	Legacy     model.Legacy
	V1Status   string
	V2Place    string
	Hint       routeHint
	Unparsed   string
}

// routeHint is a non-binding reminder. It is not a channel or model pin.
type routeHint struct {
	FrozenRoute json.RawMessage `json:"frozen_route,omitempty"`
	RunnerPref  string          `json:"runner_pref,omitempty"`
}

func (h routeHint) empty() bool {
	return !frozenPresent(h.FrozenRoute) && strings.TrimSpace(h.RunnerPref) == ""
}

func frozenPresent(raw json.RawMessage) bool {
	s := bytes.TrimSpace(raw)
	return len(s) > 0 && !bytes.Equal(s, []byte("null"))
}

// place maps one parsed record. A parse failure or an unrecognized status becomes
// non-dispatchable history and is listed, never aborted.
func place(rel string, rec *v1Record, parseErr string) placement {
	p := placement{
		Kind:     model.KindDev,
		V2Place:  "history",
		Status:   model.CardCanceled,
		Legacy:   model.Legacy{Status: "unparsed", SourcePath: rel},
		V1Status: "unparsed",
	}
	if parseErr != "" {
		p.Unparsed = rel + ": " + parseErr
		p.Legacy.Reason = parseErr
		p.Title = "(unparsed)"
		p.ProjectKey = "unassigned"
		return p
	}
	p.ProjectKey = projectKey(rec)
	p.Title = recordTitle(rec)
	p.RepoPath = firstText(rec.Dir, rec.Repo)
	if k := model.CardKind(strings.ToLower(strings.TrimSpace(rec.Type))); k.Valid() && k != "" {
		p.Kind = k
	}
	status := strings.ToLower(strings.TrimSpace(rec.Status))
	p.Legacy = model.Legacy{Status: status, SourcePath: rel}
	p.Hint = routeHint{FrozenRoute: bytes.TrimSpace(rec.FrozenRoute), RunnerPref: strings.TrimSpace(rec.RunnerPref)}
	if !frozenPresent(p.Hint.FrozenRoute) {
		p.Hint.FrozenRoute = nil
	}
	switch status {
	case "queued", "running":
		p.Status = model.CardReady
		p.V1Status = status
		p.V2Place = "ready"
	case "held":
		p.Status = model.CardPaused
		p.V1Status = status
		p.V2Place = "paused"
		p.Legacy.Reason = holdReason(rec)
	case "done":
		p.Status = model.CardDone
		p.V1Status = status
		p.V2Place = "done"
	case "failed":
		p.Status = model.CardFailed
		p.V1Status = status
		p.V2Place = "failed"
	case "canceled":
		p.Status = model.CardCanceled
		p.V1Status = status
		p.V2Place = "canceled"
	case "owner_choice":
		p.Status = model.CardCanceled
		p.V1Status = status
		p.V2Place = "history"
	default:
		why := "unrecognized status"
		if status == "" {
			why = "missing status"
		} else {
			why = "unrecognized status " + status
		}
		p.Unparsed = rel + ": " + why
		p.Legacy.Status = "unparsed"
		p.Legacy.Reason = why
		p.V1Status = "unparsed"
		p.V2Place = "history"
		p.Status = model.CardCanceled
	}
	if continuableStatus(status) {
		p.Body = continuableBody(rec.Prompts, rec.Step)
		p.Deps = mappedDeps(rec.DependsOn)
	}
	return p
}

func continuableStatus(status string) bool {
	switch status {
	case "queued", "running", "held":
		return true
	default:
		return false
	}
}

// continuableBody keeps every v1 prompt and the recorded step on the card.
// Card has no step column, so the step is the first line and stays the raw
// value, including a negative one or a step past the last prompt. A negative
// step is read as 0: nothing is completed and every prompt stays pending.
// A missing step arrives as 0. An in-range step leaves earlier prompts
// completed and the prompt at that index, internal newlines included, plus
// later prompts pending. A step past the last prompt is unknown progress:
// the body is marked 待复核, nothing is completed, and every prompt stays
// pending. Each prompt keeps its length-prefixed boundary.
func continuableBody(prompts []string, step int) string {
	effective := step
	unknown := false
	if effective < 0 {
		effective = 0
	} else if effective > len(prompts) {
		unknown = true
		effective = 0
	}
	var b strings.Builder
	b.WriteString("step: ")
	b.WriteString(strconv.Itoa(step))
	b.WriteByte('\n')
	if unknown {
		b.WriteString("待复核\n")
	}
	b.WriteString("## 已完成\n")
	writePromptSteps(&b, prompts, 0, effective)
	b.WriteString("## 待执行\n")
	if effective < len(prompts) {
		writePromptSteps(&b, prompts, effective, len(prompts))
	}
	return b.String()
}

func writePromptSteps(b *strings.Builder, prompts []string, from, to int) {
	for i := from; i < to; i++ {
		b.WriteString("<<<step ")
		b.WriteString(strconv.Itoa(i))
		b.WriteByte(' ')
		b.WriteString(strconv.Itoa(len(prompts[i])))
		b.WriteString(">>>\n")
		b.WriteString(prompts[i])
		b.WriteByte('\n')
	}
}

func mappedDeps(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		out = append(out, cardID("", id))
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func projectKey(rec *v1Record) string {
	if s := strings.TrimSpace(rec.Project); s != "" {
		return s
	}
	if s := strings.TrimSpace(rec.ModuleID); s != "" {
		return s
	}
	return "unassigned"
}

func recordTitle(rec *v1Record) string {
	if s := strings.TrimSpace(rec.Title); s != "" {
		return s
	}
	if s := strings.TrimSpace(string(rec.Goal)); s != "" {
		return s
	}
	if s := strings.TrimSpace(rec.ID); s != "" {
		return s
	}
	return "(v1)"
}

func holdReason(rec *v1Record) string {
	for _, s := range []string{rec.HoldReason, rec.Reason, rec.LastError} {
		if t := strings.TrimSpace(s); t != "" {
			return t
		}
	}
	return ""
}

func firstText(values ...string) string {
	for _, s := range values {
		if t := strings.TrimSpace(s); t != "" {
			return t
		}
	}
	return ""
}

// cardID preserves an explicit id under v1-. A file with no id is named from
// its path under v1g-. That prefix cannot equal v1- plus any explicit id, so
// a generated token is not dropped when it matches another record's id.
func cardID(rel, id string) string {
	if t := strings.TrimSpace(id); t != "" {
		return "v1-" + t
	}
	return "v1g-" + sanitize(rel)
}

func objectiveID(project string) string {
	return "v1-import-" + sanitize(project)
}

func projectID(project string) string {
	return "v1proj-" + sanitize(project)
}

// sanitize keeps a safe token that is not itself a generated encoding.
// Every other value becomes g plus the hex of the original. That encoding
// is not a preserved token, so a generated id cannot equal a raw one.
func sanitize(s string) string {
	if isPreservedToken(s) {
		return s
	}
	return "g" + hex.EncodeToString([]byte(s))
}

func isPreservedToken(s string) bool {
	if s == "" || isGeneratedToken(s) {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

func isGeneratedToken(s string) bool {
	if len(s) == 0 || s[0] != 'g' {
		return false
	}
	rest := s[1:]
	if len(rest)%2 != 0 {
		return false
	}
	_, err := hex.DecodeString(rest)
	return err == nil
}
