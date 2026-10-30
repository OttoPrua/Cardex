package quota

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"cardex/internal/model"
)

// estimatedConfidence is below the default confidence of a measured token total.
const estimatedConfidence = 0.5

// Ledger is the local quota estimator. It keeps windows and blocks in memory.
type Ledger struct {
	mu  sync.Mutex
	now func() time.Time
	ch  map[string]*chState
}

type chState struct {
	windows    []windowSpec
	events     []usageEvent
	blockUntil time.Time
}

type usageEvent struct {
	At         time.Time
	Tokens     int64
	Confidence float64
}

// New returns an empty estimator. Configure channels before recording.
func New() *Ledger {
	return &Ledger{now: time.Now, ch: map[string]*chState{}}
}

// SetClock replaces the clock used to decide which window is open.
func (l *Ledger) SetClock(now func() time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if now == nil {
		l.now = time.Now
		return
	}
	l.now = now
}

// Configure installs the channel's quota windows. Events and blocks are kept.
func (l *Ledger) Configure(ch model.Channel) {
	l.mu.Lock()
	defer l.mu.Unlock()
	st := l.state(ch.ID)
	specs := make([]windowSpec, 0, len(ch.QuotaWindows))
	for _, w := range ch.QuotaWindows {
		name := canonName(w.Name)
		if name == "" || w.LimitTokens <= 0 {
			continue
		}
		specs = append(specs, windowSpec{
			Name:   name,
			Rule:   canonRule(w.ResetRule),
			Anchor: w.ResetAt.UTC(),
			Limit:  w.LimitTokens,
		})
	}
	st.windows = specs
}

func (l *Ledger) state(id string) *chState {
	st := l.ch[id]
	if st == nil {
		st = &chState{}
		l.ch[id] = st
	}
	return st
}

// Remaining reports the tightest limited window, or unknown when none has a token limit.
func (l *Ledger) Remaining(ctx context.Context, channelID string) (Estimate, error) {
	if err := ctx.Err(); err != nil {
		return Estimate{}, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now().UTC()
	st := l.ch[channelID]
	if st == nil {
		return Estimate{Unknown: true}, nil
	}
	est := Estimate{Unknown: true}
	var (
		have       bool
		bestRemain int64
		bestLimit  int64
		bestEnd    time.Time
		bestConf   float64
		bestUsed   int64
	)
	for _, w := range st.windows {
		start, end, ok := w.bounds(now)
		if !ok {
			continue
		}
		used, conf, saw := sumEvents(st.events, start, end)
		if !saw {
			conf = 1
		}
		remain := w.Limit - used
		if !have || tighter(remain, w.Limit, bestRemain, bestLimit) || (sameRatio(remain, w.Limit, bestRemain, bestLimit) && end.Before(bestEnd)) {
			have = true
			bestRemain = remain
			bestLimit = w.Limit
			bestEnd = end
			bestConf = conf
			bestUsed = used
		}
	}
	if have {
		est.Unknown = false
		est.Ratio = float64(bestRemain) / float64(bestLimit)
		est.Reset = bestEnd
		est.Confidence = bestConf
		est.UsedTokens = bestUsed
		est.LimitTokens = bestLimit
	}
	if !st.blockUntil.IsZero() && now.Before(st.blockUntil) {
		est.Blocked = true
		est.Reset = st.blockUntil
	}
	return est, nil
}

// Record adds usage that falls inside each window covering ended.
// Unknown usage is a zero token total with an empty source: the amount grows with the attempt duration and confidence is lower.
func (l *Ledger) Record(ctx context.Context, channelID string, usage model.Usage, started, ended time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if channelID == "" {
		return fmt.Errorf("channel required")
	}
	known := usage.Tokens > 0 || strings.TrimSpace(usage.Source) != ""
	var tokens int64
	var conf float64
	if known {
		tokens = usage.Tokens
		if usage.Confidence > 0 {
			conf = usage.Confidence
		} else {
			conf = 1
		}
	} else {
		sec := int64(ended.Sub(started) / time.Second)
		if sec < 1 {
			sec = 1
		}
		tokens = sec
		conf = estimatedConfidence
	}
	at := ended
	if at.IsZero() {
		at = started
	}
	l.mu.Lock()
	if at.IsZero() {
		at = l.now()
	}
	st := l.state(channelID)
	st.events = append(st.events, usageEvent{At: at.UTC(), Tokens: tokens, Confidence: conf})
	l.mu.Unlock()
	return nil
}

// ObserveLimit hard-blocks the channel until reset and moves each window anchor to that reset.
func (l *Ledger) ObserveLimit(ctx context.Context, channelID string, reset time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if channelID == "" || reset.IsZero() {
		return fmt.Errorf("observe limit requires a channel and a reset time")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	st := l.state(channelID)
	st.blockUntil = reset.UTC()
	for i := range st.windows {
		st.windows[i].Anchor = reset.UTC()
	}
	return nil
}

func tighter(remainA, limitA, remainB, limitB int64) bool {
	return remainA*limitB < remainB*limitA
}

func sameRatio(remainA, limitA, remainB, limitB int64) bool {
	return remainA*limitB == remainB*limitA
}

func sumEvents(events []usageEvent, start, end time.Time) (int64, float64, bool) {
	var used int64
	var conf float64
	saw := false
	for _, e := range events {
		if e.At.Before(start) || !e.At.Before(end) {
			continue
		}
		used += e.Tokens
		if !saw || e.Confidence < conf {
			conf = e.Confidence
		}
		saw = true
	}
	return used, conf, saw
}

var _ Estimator = (*Ledger)(nil)
