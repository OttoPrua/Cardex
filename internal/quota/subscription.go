package quota

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const subscriptionMaxBytes = 1 << 20
const SubscriptionMaxAge = 15 * time.Minute

// Subscription is a credential-free CodexBar measurement, separate from the routing ledger.
type Subscription struct {
	Provider   string               `json:"provider"`
	Source     string               `json:"source"`
	CapturedAt time.Time            `json:"captured_at"`
	State      string               `json:"state"`
	Windows    []SubscriptionWindow `json:"windows"`
	Credits    *float64             `json:"credits,omitempty"`
	CreditsAt  time.Time            `json:"credits_at,omitempty"`
}

type SubscriptionWindow struct {
	Name      string    `json:"name"`
	Remaining *float64  `json:"remaining"`
	ResetAt   time.Time `json:"reset_at"`
}

type codexBarWindow struct {
	UsedPercent *float64  `json:"usedPercent"`
	Title       string    `json:"title"`
	Minutes     int       `json:"windowMinutes"`
	ResetAt     time.Time `json:"resetsAt"`
	Placeholder bool      `json:"isSyntheticPlaceholder"`
}

// ParseCodexBar accepts exactly one explicitly selected provider/account. Identity,
// diagnostics, cost estimates and unrecognized fields are never retained.
func ParseCodexBar(r io.Reader, provider string, now time.Time) (Subscription, error) {
	var rows []struct {
		Provider string          `json:"provider"`
		Source   string          `json:"source"`
		Error    json.RawMessage `json:"error"`
		Usage    *struct {
			UpdatedAt  time.Time       `json:"updatedAt"`
			Confidence string          `json:"dataConfidence"`
			Primary    *codexBarWindow `json:"primary"`
			Secondary  *codexBarWindow `json:"secondary"`
			Tertiary   *codexBarWindow `json:"tertiary"`
			Extra      []struct {
				Title  string         `json:"title"`
				Known  *bool          `json:"usageKnown"`
				Window codexBarWindow `json:"window"`
			} `json:"extraRateWindows"`
		} `json:"usage"`
		Labels struct {
			Primary   string `json:"primary"`
			Secondary string `json:"secondary"`
			Tertiary  string `json:"tertiary"`
		} `json:"rateWindowLabels"`
		Credits *struct {
			Remaining *float64  `json:"remaining"`
			UpdatedAt time.Time `json:"updatedAt"`
			Succeeded *bool     `json:"balanceReadSucceeded"`
		} `json:"credits"`
	}
	b, err := io.ReadAll(io.LimitReader(r, subscriptionMaxBytes+1))
	if err != nil || len(b) > subscriptionMaxBytes || json.Unmarshal(b, &rows) != nil {
		return Subscription{}, errors.New("invalid CodexBar usage JSON (maximum 1 MiB)")
	}
	if len(rows) != 1 || rows[0].Provider != provider || !subscriptionProvider(provider) {
		return Subscription{}, errors.New("select exactly one matching CodexBar provider and account")
	}
	row := rows[0]
	s := Subscription{Provider: provider, State: "unavailable", Windows: []SubscriptionWindow{}}
	if !subscriptionSource(provider, row.Source) {
		s.State = "unsupported_source"
		return s, nil
	}
	s.Source = row.Source
	if len(row.Error) != 0 && string(row.Error) != "null" {
		return s, nil
	}
	if u := row.Usage; u != nil {
		s.CapturedAt = u.UpdatedAt
		known := u.Confidence == "" || u.Confidence == "exact" || u.Confidence == "percentOnly"
		add := func(w *codexBarWindow, label string, reported bool) {
			if w == nil || len(s.Windows) >= 32 {
				return
			}
			if w.Title != "" {
				label = w.Title
			}
			if label == "" && w.Minutes > 0 {
				label = fmt.Sprintf("%d min", w.Minutes)
			}
			if label == "" {
				label = "quota"
			}
			label = string([]rune(label)[:min(len([]rune(label)), 96)])
			v := SubscriptionWindow{Name: label, ResetAt: w.ResetAt}
			if known && reported && !w.Placeholder && w.UsedPercent != nil && finite(*w.UsedPercent) && *w.UsedPercent >= 0 && *w.UsedPercent <= 100 {
				left := math.Max(0, 1-*w.UsedPercent/100)
				v.Remaining = &left
			}
			s.Windows = append(s.Windows, v)
		}
		add(u.Primary, row.Labels.Primary, true)
		add(u.Secondary, row.Labels.Secondary, true)
		add(u.Tertiary, row.Labels.Tertiary, true)
		for _, extra := range u.Extra {
			add(&extra.Window, extra.Title, extra.Known == nil || *extra.Known)
		}
	}
	// Codex credits are not dollars or another provider's balance.
	if c := row.Credits; provider == "codex" && c != nil && (c.Succeeded == nil || *c.Succeeded) && c.Remaining != nil && finite(*c.Remaining) && *c.Remaining >= 0 {
		s.Credits, s.CreditsAt = c.Remaining, c.UpdatedAt
	}
	return s.current(now), nil
}

func subscriptionProvider(provider string) bool {
	switch provider {
	case "codex", "claude", "cursor", "grok", "gemini", "kimi":
		return true
	}
	return false
}

func subscriptionSource(provider, source string) bool {
	switch provider {
	case "codex":
		return source == "codex-cli" || source == "oauth"
	case "claude":
		return source == "oauth"
	case "cursor":
		return source == "web"
	case "grok":
		return source == "grok-cli" || source == "grok-cli-proxy"
	case "gemini":
		return source == "oauth-api"
	case "kimi":
		return source == "Kimi Code API key" || source == "Kimi Code CLI"
	}
	return false
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func subscriptionFresh(at, now time.Time) bool {
	return !at.IsZero() && !at.After(now.Add(time.Minute)) && now.Sub(at) <= SubscriptionMaxAge
}

func (s Subscription) current(now time.Time) Subscription {
	if !subscriptionSource(s.Provider, s.Source) || s.State == "unsupported_source" {
		return Subscription{Provider: s.Provider, State: "unsupported_source"}
	}
	s.State = "unavailable"
	if !s.CapturedAt.IsZero() && !subscriptionFresh(s.CapturedAt, now) {
		s.State = "stale"
	}
	for i := range s.Windows {
		w := &s.Windows[i]
		if !subscriptionFresh(s.CapturedAt, now) || w.Remaining == nil || !finite(*w.Remaining) || *w.Remaining < 0 || *w.Remaining > 1 || (!w.ResetAt.IsZero() && !w.ResetAt.After(now)) {
			w.Remaining = nil
		} else {
			s.State = "known"
		}
	}
	if s.Credits != nil && (s.Provider != "codex" || !finite(*s.Credits) || *s.Credits < 0 || !subscriptionFresh(s.CreditsAt, now)) {
		s.Credits = nil
	}
	return s
}

func subscriptionPath(home, group, channel string) string {
	key := "channel:" + channel
	if strings.TrimSpace(group) != "" {
		key = "group:" + group
	}
	return filepath.Join(home, "subscription-usage", fmt.Sprintf("%x.json", sha256.Sum256([]byte(key))))
}

func SaveSubscription(home, group, channel string, s Subscription) error {
	path := subscriptionPath(home, group, channel)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".usage-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := json.NewEncoder(f).Encode(s); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func ReadSubscription(home, group, channel string, now time.Time) (*Subscription, error) {
	f, err := os.Open(subscriptionPath(home, group, channel))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var s Subscription
	b, err := io.ReadAll(io.LimitReader(f, subscriptionMaxBytes+1))
	if err != nil || len(b) > subscriptionMaxBytes || json.Unmarshal(b, &s) != nil {
		return nil, errors.New("invalid subscription snapshot")
	}
	s = s.current(now)
	return &s, nil
}
