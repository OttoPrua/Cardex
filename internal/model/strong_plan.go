package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

const StrongTemplateStrong = "strong.v1"
const StrongTemplateLight = "light.v1"
const StrongPlanVersion = 1
const StrongTokenCeiling int64 = 100_000_000

var ErrStrongPlan = errors.New("strong-thinking plan or approval invalid")

type StrongPhase string

const (
	StrongPhaseFrame      StrongPhase = "frame"
	StrongPhaseEvidence   StrongPhase = "evidence"
	StrongPhaseBlind      StrongPhase = "blind"
	StrongPhasePeer       StrongPhase = "peer"
	StrongPhaseVerify     StrongPhase = "verify"
	StrongPhaseSynthesis  StrongPhase = "synthesis"
	StrongPhaseCheckpoint StrongPhase = "st3_checkpoint"
	StrongPhaseReview     StrongPhase = "review"
	StrongPhaseRewrite    StrongPhase = "rewrite"
)

type StrongPlan struct {
	Run               string                     `json:"run"`
	Revision          int                        `json:"revision"`
	Version           int                        `json:"version"`
	Template          string                     `json:"template"`
	Project           string                     `json:"project"`
	Scope             string                     `json:"scope"`
	TaskSHA           string                     `json:"task_sha"`
	EvidenceSHA       string                     `json:"evidence_sha"`
	EvidenceCommit    string                     `json:"evidence_commit"`
	EvidencePaths     []string                   `json:"evidence_paths"`
	GlobalTokens      int64                      `json:"global_tokens"`
	Seats             int                        `json:"seats"`
	Framing           bool                       `json:"framing"`
	SecondPeerRound   bool                       `json:"second_peer_round"`
	CrossFamilyReview bool                       `json:"cross_family_review,omitempty"`
	TargetedVerify    bool                       `json:"targeted_verify"`
	Limits            map[string]StrongSlotLimit `json:"limits"`
}

type StrongApprovedRoute struct {
	Runner  string `json:"runner"`
	Channel string `json:"channel"`
	Model   string `json:"model"`
	Family  string `json:"family"`
	T0      bool   `json:"t0"`
}

// StrongConcreteRoute is the historical concrete identity of a synthetic
// execution, independent of its family's mutable registry label.
type StrongConcreteRoute struct{ Runner, Channel, Model string }

func (r StrongApprovedRoute) Concrete() StrongConcreteRoute {
	return StrongConcreteRoute{strings.ToLower(r.Runner), strings.ToLower(r.Channel), strings.ToLower(r.Model)}
}

func (r StrongConcreteRoute) ModelKey() string { return r.Runner + ":" + r.Model }

type StrongSlotLimit struct {
	Tokens int64                 `json:"tokens"`
	Routes []StrongApprovedRoute `json:"routes"`
}

type StrongSlot struct {
	Key       string
	Phase     StrongPhase
	Seat      string
	Round     int
	Deps      []string
	Condition string
}

const StrongConditionNewObjection = "new_material_objection"
const StrongConditionVerifiableDispute = "verifiable_dispute"

func (p StrongPlan) Graph() ([]StrongSlot, error) {
	switch p.Template {
	case StrongTemplateLight:
		if p.Seats != 1 || p.Framing || p.SecondPeerRound || p.TargetedVerify {
			return nil, ErrStrongPlan
		}
		return p.reviewTail([]StrongSlot{
			{Key: "evidence", Phase: StrongPhaseEvidence},
			{Key: "answer", Phase: StrongPhaseBlind, Seat: "seat-1", Deps: []string{"evidence"}},
		}, "answer", "seat-1"), nil
	case StrongTemplateStrong:
		if p.Seats < 2 || p.Seats > 4 {
			return nil, ErrStrongPlan
		}
	default:
		return nil, ErrStrongPlan
	}
	var slots []StrongSlot
	evidence := StrongSlot{Key: "evidence", Phase: StrongPhaseEvidence}
	if p.Framing {
		slots = append(slots, StrongSlot{Key: "frame", Phase: StrongPhaseFrame})
		evidence.Deps = []string{"frame"}
	}
	slots = append(slots, evidence)
	var blind, peer, peer2 []string
	for i := 1; i <= p.Seats; i++ {
		seat := fmt.Sprintf("seat-%d", i)
		k := fmt.Sprintf("blind-%d", i)
		blind = append(blind, k)
		slots = append(slots, StrongSlot{Key: k, Phase: StrongPhaseBlind, Seat: seat, Deps: []string{"evidence"}})
	}
	for i := 1; i <= p.Seats; i++ {
		k := fmt.Sprintf("peer-1-%d", i)
		peer = append(peer, k)
		slots = append(slots, StrongSlot{Key: k, Phase: StrongPhasePeer, Seat: fmt.Sprintf("seat-%d", i), Round: 1, Deps: slices.Clone(blind)})
	}
	lead := slices.Clone(peer)
	if p.SecondPeerRound {
		for i := 1; i <= p.Seats; i++ {
			k := fmt.Sprintf("peer-2-%d", i)
			peer2 = append(peer2, k)
			slots = append(slots, StrongSlot{Key: k, Phase: StrongPhasePeer, Seat: fmt.Sprintf("seat-%d", i), Round: 2, Deps: slices.Clone(peer), Condition: StrongConditionNewObjection})
		}
		lead = slices.Clone(peer2)
	}
	synthesisDeps := append(slices.Clone(blind), peer...)
	synthesisDeps = append(synthesisDeps, peer2...)
	synthesisDeps = append(synthesisDeps, "evidence")
	if p.TargetedVerify {
		slots = append(slots, StrongSlot{Key: "verify", Phase: StrongPhaseVerify, Deps: lead, Condition: StrongConditionVerifiableDispute})
		synthesisDeps = append(synthesisDeps, "verify")
	}
	seat := ""
	if p.CrossFamilyReview {
		seat = "author-seat"
	}
	slots = append(slots, StrongSlot{Key: "synthesis", Phase: StrongPhaseSynthesis, Seat: seat, Deps: synthesisDeps})
	return p.reviewTail(slots, "synthesis", seat), nil
}

func (p StrongPlan) reviewTail(slots []StrongSlot, author, seat string) []StrongSlot {
	if !p.CrossFamilyReview {
		return append(slots, StrongSlot{Key: "st3", Phase: StrongPhaseCheckpoint, Deps: []string{author}})
	}
	return append(slots,
		StrongSlot{Key: "review", Phase: StrongPhaseReview, Seat: "reviewer-seat", Deps: []string{author, "evidence"}},
		StrongSlot{Key: "rewrite", Phase: StrongPhaseRewrite, Seat: seat, Deps: []string{author, "review", "evidence"}},
	)
}

func (p StrongPlan) StrongAuthorSlot() string {
	if p.Template == StrongTemplateLight {
		return "answer"
	}
	return "synthesis"
}

func (p StrongPlan) Canonical() ([]byte, string, error) {
	graph, err := p.Graph()
	wantLimits := len(graph)
	if !p.CrossFamilyReview {
		wantLimits--
	}
	if err != nil || p.Version != StrongPlanVersion || p.Revision < 1 || p.Revision > 10000 ||
		!StrongKey(p.Run) || !StrongKey(p.Project) || !StrongResultBody([]byte(p.Scope)) || len(p.Scope) > 4096 || strings.TrimSpace(p.Scope) != p.Scope || strings.ContainsAny(p.Scope, "\r\n") ||
		!StrongDigestKey(p.TaskSHA) || !StrongDigestKey(p.EvidenceSHA) || !strongCommit(p.EvidenceCommit) || len(p.EvidencePaths) == 0 || len(p.EvidencePaths) > 16 ||
		p.GlobalTokens <= 0 || p.GlobalTokens > StrongTokenCeiling || len(p.Limits) != wantLimits {
		return nil, "", ErrStrongPlan
	}
	seen := map[string]bool{}
	for _, path := range p.EvidencePaths {
		if path == "" || path == "." || filepath.IsAbs(path) || strings.ContainsAny(path, "\\:\x00\r\n\t") || len(path) > 1024 || seen[path] {
			return nil, "", ErrStrongPlan
		}
		for _, part := range strings.Split(path, "/") {
			if part == "." || part == ".." || part == "" {
				return nil, "", ErrStrongPlan
			}
		}
		seen[path] = true
	}
	// Ordering is significant for source and route choice. The approval hashes
	// exactly that order; JSON sorts only the map keys.
	for _, slot := range graph {
		if slot.Phase == StrongPhaseCheckpoint {
			continue
		}
		lim, ok := p.Limits[slot.Key]
		if !ok || lim.Tokens <= 0 || lim.Tokens > p.GlobalTokens || len(lim.Routes) == 0 || len(lim.Routes) > 8 {
			return nil, "", ErrStrongPlan
		}
		seenRoutes := map[StrongApprovedRoute]bool{}
		for _, r := range lim.Routes {
			if !StrongKey(r.Runner) || !StrongKey(r.Channel) || !StrongKey(r.Model) || !StrongKey(r.Family) || seenRoutes[r] {
				return nil, "", ErrStrongPlan
			}
			seenRoutes[r] = true
		}
	}
	if p.CrossFamilyReview {
		modelFamilies := map[string]string{}
		routeFamilies := map[StrongConcreteRoute]string{}
		for _, limit := range p.Limits {
			for _, route := range limit.Routes {
				id, family := route.Concrete(), strings.ToLower(route.Family)
				if old := modelFamilies[id.ModelKey()]; old != "" && old != family {
					return nil, "", ErrStrongPlan
				}
				if old := routeFamilies[id]; old != "" && old != family {
					return nil, "", ErrStrongPlan
				}
				modelFamilies[id.ModelKey()], routeFamilies[id] = family, family
			}
		}
		for _, route := range p.Limits["rewrite"].Routes {
			if !slices.Contains(p.Limits[p.StrongAuthorSlot()].Routes, route) || route.T0 || route.Runner != string(RunnerFake) {
				return nil, "", ErrStrongPlan
			}
		}
	}
	body, err := json.Marshal(p)
	if err != nil || len(body) > 64<<10 {
		return nil, "", ErrStrongPlan
	}
	return body, StrongArtifactDigest(body), nil
}

func strongCommit(id string) bool {
	return (len(id) == 40 || len(id) == 64) && strings.Trim(id, "0123456789abcdef") == ""
}

func (p StrongPlan) Slot(key string) (StrongSlot, bool) {
	graph, err := p.Graph()
	if err != nil {
		return StrongSlot{}, false
	}
	for _, s := range graph {
		if s.Key == key {
			return s, true
		}
	}
	return StrongSlot{}, false
}
