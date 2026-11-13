package board

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
)

// projectProgress groups the active objectives of one project for the
// overview at the top of the current-work page.
type projectProgress struct {
	Project    string              `json:"project"`
	Objectives []objectiveProgress `json:"objectives"`
	// Wrapping are objectives whose cards are all done, waiting for acceptance.
	Wrapping []objectiveProgress `json:"wrapping"`
	Done     int                 `json:"done"`
	Total    int                 `json:"total"`
	Running  int                 `json:"running"`
}

// objectiveProgress is how far one active objective is and what it is doing now.
type objectiveProgress struct {
	ID          string        `json:"id"`
	Title       string        `json:"title"`
	Stage       string        `json:"stage"`
	StageTone   string        `json:"stage_tone"`
	Now         string        `json:"now"`
	Total       int           `json:"total"`
	Done        int           `json:"done"`
	Active      int           `json:"active"`
	Merging     int           `json:"merging"`
	Waiting     int           `json:"waiting"`
	Ready       int           `json:"ready"`
	NeedsInput  int           `json:"needs_input"`
	Failed      int           `json:"failed"`
	Paused      int           `json:"paused"`
	Questions   int           `json:"questions"`
	NodesTotal  int           `json:"nodes_total"`
	NodesFrozen int           `json:"nodes_frozen"`
	Running     []runningLane `json:"running"`
	ETA         string        `json:"eta"`
	ETABasis    string        `json:"eta_basis"`
}

// runningLane is one attempt running right now, for the parallel lanes.
type runningLane struct {
	Attempt   string `json:"attempt"`
	Card      string `json:"card"`
	CardTitle string `json:"card_title"`
	Objective string `json:"objective_title"`
	Project   string `json:"project"`
	Channel   string `json:"channel"`
	Model     string `json:"model"`
	Status    string `json:"status"`
	Elapsed   string `json:"elapsed"`
	Typical   string `json:"typical"`
	// Fraction is elapsed over the typical attempt time, capped at 1; nil when unknown.
	Fraction *float64 `json:"fraction"`
	Over     bool     `json:"over"`
}

func (p objectiveProgress) Percent() int {
	if p.Total == 0 {
		return 0
	}
	return int(math.Round(float64(p.Done) * 100 / float64(p.Total)))
}

func (p projectProgress) Percent() int {
	if p.Total == 0 {
		return 0
	}
	return int(math.Round(float64(p.Done) * 100 / float64(p.Total)))
}

// seg returns the CSS width of n out of the objective total.
func (p objectiveProgress) Seg(n int) string {
	if p.Total == 0 || n <= 0 {
		return "0%"
	}
	return fmt.Sprintf("%.2f%%", float64(n)*100/float64(p.Total))
}

type progressInput struct {
	objective model.Objective
	cards     []model.Card
	nodes     []model.DesignNode
	questions int
}

func buildProgress(inputs []progressInput, attempts []model.Attempt, now time.Time) ([]projectProgress, []runningLane) {
	cardObj := map[string]progressInput{}
	cardByID := map[string]model.Card{}
	for _, in := range inputs {
		for _, c := range in.cards {
			cardObj[c.ID] = in
			cardByID[c.ID] = c
		}
	}
	// Typical attempt duration: median of ended attempts, per objective and overall.
	perObj := map[string][]time.Duration{}
	var all []time.Duration
	for _, a := range attempts {
		if a.StartedAt.IsZero() || a.EndedAt.IsZero() || !a.EndedAt.After(a.StartedAt) {
			continue
		}
		// Only attempts that delivered: the card ended merged or done. Instant
		// failures and timeouts would skew the typical time.
		if c, ok := cardByID[a.Card]; !ok || (c.Status != model.CardMerged && c.Status != model.CardDone) {
			continue
		}
		d := a.EndedAt.Sub(a.StartedAt)
		if d < time.Minute {
			continue
		}
		all = append(all, d)
		if in, ok := cardObj[a.Card]; ok {
			perObj[in.objective.ID] = append(perObj[in.objective.ID], d)
		}
	}
	overall := median(all)

	lanesByObj := map[string][]runningLane{}
	var lanes []runningLane
	for _, a := range attempts {
		if !a.EndedAt.IsZero() {
			continue
		}
		c, ok := cardByID[a.Card]
		if !ok || !currentCard(c) {
			continue
		}
		in := cardObj[a.Card]
		typical := median(perObj[in.objective.ID])
		if typical == 0 {
			typical = overall
		}
		l := runningLane{Attempt: a.ID, Card: c.ID, CardTitle: c.Title, Objective: in.objective.Title, Project: in.objective.Project,
			Channel: a.Route.Channel, Model: a.Route.Model, Status: string(c.Status)}
		if !a.StartedAt.IsZero() {
			el := now.Sub(a.StartedAt)
			l.Elapsed = humanDuration(el)
			if typical > 0 {
				l.Typical = humanDuration(typical)
				f := float64(el) / float64(typical)
				l.Over = f > 1
				if f > 1 {
					f = 1
				}
				l.Fraction = &f
			}
		}
		lanes = append(lanes, l)
		lanesByObj[in.objective.ID] = append(lanesByObj[in.objective.ID], l)
	}

	byProject := map[string]*projectProgress{}
	var order []string
	for _, in := range inputs {
		p := objectiveProgress{ID: in.objective.ID, Title: in.objective.Title, Questions: in.questions, Running: lanesByObj[in.objective.ID]}
		if p.Running == nil {
			p.Running = []runningLane{}
		}
		p.NodesTotal = len(in.nodes)
		for _, n := range in.nodes {
			if n.Status == model.NodeFrozen {
				p.NodesFrozen++
			}
		}
		for _, c := range in.cards {
			if legacyImported(c) || c.Status == model.CardCanceled {
				continue
			}
			p.Total++
			switch c.Status {
			case model.CardMerged, model.CardDone:
				p.Done++
			case model.CardRunning, model.CardHarvesting, model.CardReviewing:
				p.Active++
			case model.CardMergeQueued:
				p.Merging++
			case model.CardWaiting:
				p.Waiting++
			case model.CardReady:
				p.Ready++
			case model.CardNeedsInput:
				p.NeedsInput++
			case model.CardFailed:
				p.Failed++
			case model.CardPaused:
				p.Paused++
			}
		}
		p.Stage, p.StageTone, p.Now = stageOf(p)
		remaining := p.Total - p.Done - p.Paused
		typical := median(perObj[in.objective.ID])
		basis := "本目标"
		if typical == 0 {
			typical, basis = overall, "全局"
		}
		switch {
		case p.Total > 0 && remaining <= 0 && p.Paused > 0:
			p.ETA = fmt.Sprintf("其余 %d 张卡暂停中", p.Paused)
		case p.Total > 0 && remaining <= 0:
			p.ETA = "卡已全部完成"
		case p.Total == 0:
			p.ETA = "还没拆出卡，暂无法估算"
		case typical == 0:
			p.ETA = "暂无历史耗时，无法估算"
		default:
			par := p.Active
			if par < 1 {
				par = 1
			}
			rounds := int(math.Ceil(float64(remaining) / float64(par)))
			p.ETA = "约 " + humanDuration(time.Duration(rounds)*typical)
			p.ETABasis = fmt.Sprintf("剩 %d 张卡 × %s单卡中位耗时 %s ÷ 并行 %d，不含等你拍板和排队的时间", remaining, basis, humanDuration(typical), par)
		}
		proj := in.objective.Project
		if proj == "" {
			proj = "未归属项目"
		}
		pp, ok := byProject[proj]
		if !ok {
			pp = &projectProgress{Project: proj, Objectives: []objectiveProgress{}, Wrapping: []objectiveProgress{}}
			byProject[proj] = pp
			order = append(order, proj)
		}
		if p.Stage == stageWrapping {
			pp.Wrapping = append(pp.Wrapping, p)
		} else {
			pp.Objectives = append(pp.Objectives, p)
		}
		pp.Done += p.Done
		pp.Total += p.Total
		pp.Running += len(p.Running)
	}
	out := make([]projectProgress, 0, len(order))
	for _, k := range order {
		out = append(out, *byProject[k])
	}
	for i := range out {
		sort.SliceStable(out[i].Objectives, func(a, b int) bool {
			return stageRank[out[i].Objectives[a].Stage] < stageRank[out[i].Objectives[b].Stage]
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Running != out[j].Running {
			return out[i].Running > out[j].Running
		}
		if len(out[i].Objectives) != len(out[j].Objectives) {
			return len(out[i].Objectives) > len(out[j].Objectives)
		}
		return out[i].Percent() < out[j].Percent()
	})
	if lanes == nil {
		lanes = []runningLane{}
	}
	return out, lanes
}

func stageOf(p objectiveProgress) (stage, tone, now string) {
	switch {
	case p.Total == 0 && p.NodesTotal > 0 && p.NodesFrozen < p.NodesTotal:
		return "设计中", "live", fmt.Sprintf("设计树已冻结 %d / %d 个节点", p.NodesFrozen, p.NodesTotal)
	case p.Total == 0:
		return "待拆卡", "mute", "还没有可执行的卡"
	case p.Active > 0:
		now = fmt.Sprintf("%d 张卡正在执行", p.Active)
		if p.Running != nil && len(p.Running) > 0 {
			now = "正在做：" + p.Running[0].CardTitle
			if len(p.Running) > 1 {
				now += fmt.Sprintf(" 等 %d 张", len(p.Running))
			}
		}
		return "开发中", "live", now
	case p.Merging > 0:
		return "合并中", "live", fmt.Sprintf("%d 张卡排队合并", p.Merging)
	case p.NeedsInput > 0 || p.Questions > 0:
		var parts []string
		if p.NeedsInput > 0 {
			parts = append(parts, fmt.Sprintf("%d 张卡待复核或拍板", p.NeedsInput))
		}
		if p.Questions > 0 {
			parts = append(parts, fmt.Sprintf("%d 个问题等你回答", p.Questions))
		}
		return "待拍板", "warn", strings.Join(parts, "，")
	case p.Ready > 0:
		return "排队派发", "mute", fmt.Sprintf("%d 张卡等空闲渠道", p.Ready)
	case p.Waiting > 0:
		return "等待依赖", "warn", fmt.Sprintf("%d 张卡被依赖或声明挡住", p.Waiting)
	case p.Failed > 0:
		return "有失败", "err", fmt.Sprintf("%d 张卡失败，需要处理", p.Failed)
	case p.Done >= p.Total-p.Paused:
		return stageWrapping, "ok", "卡已全部完成，等目标验收"
	default:
		return "暂停", "mute", fmt.Sprintf("%d 张卡暂停", p.Paused)
	}
}

const stageWrapping = "收尾"

var stageRank = map[string]int{"开发中": 0, "合并中": 1, "待拍板": 2, "有失败": 3, "等待依赖": 4, "排队派发": 5, "设计中": 6, "待拆卡": 7, "暂停": 8, stageWrapping: 9}

func median(ds []time.Duration) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	s := append([]time.Duration(nil), ds...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[len(s)/2]
}

func humanDuration(d time.Duration) string {
	if d < time.Minute {
		return "不到 1 分钟"
	}
	m := int(d.Round(time.Minute) / time.Minute)
	if m < 60 {
		return fmt.Sprintf("%d 分钟", m)
	}
	h, m := m/60, m%60
	if h < 48 {
		if m == 0 {
			return fmt.Sprintf("%d 小时", h)
		}
		return fmt.Sprintf("%d 小时 %d 分", h, m)
	}
	return fmt.Sprintf("%d 天 %d 小时", h/24, h%24)
}
