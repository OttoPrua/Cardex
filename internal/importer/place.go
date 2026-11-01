package importer

import (
	"bytes"
	"encoding/json"
	"strings"

	"cardex/internal/model"
)

// importObjectiveTitle is the single manual objective created per v1 project.
const importObjectiveTitle = "v1 导入"

// v1Record is the subset of a v1 task or workflow JSON object this importer reads.
type v1Record struct {
	ID          string          `json:"id"`
	Title       string          `json:"title"`
	Goal        string          `json:"goal"`
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
}

// placement is the v2 row a parsed record becomes. It does not touch the disk.
type placement struct {
	ProjectKey string
	Title      string
	Kind       model.CardKind
	RepoPath   string
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
	return p
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
	if s := strings.TrimSpace(rec.Goal); s != "" {
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

func cardID(rel, id string) string {
	if t := strings.TrimSpace(id); t != "" {
		return "v1-" + t
	}
	return "v1-" + sanitize(rel)
}

func objectiveID(project string) string {
	return "v1-import-" + sanitize(project)
}

func projectID(project string) string {
	return "v1proj-" + sanitize(project)
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "x"
	}
	return out
}
