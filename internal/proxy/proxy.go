// Package proxy is Cardex's one place for outbound proxy settings.
//
// config.json may hold:
//
//	"proxy": {
//	  "url": "http://127.0.0.1:6152",
//	  "socks": "socks5://127.0.0.1:6153",
//	  "no_proxy": ["127.0.0.1", "localhost", "100.64.0.0/10", ".ts.net"],
//	  "channels": {"kimi": "direct", "agy": "http://127.0.0.1:7890"}
//	}
//
// Without a "proxy" section nothing changes: children inherit whatever the
// serve environment already has. A channel set to "direct" runs with every
// proxy variable cleared.
package proxy

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Direct is the per-channel value that clears every proxy variable.
const Direct = "direct"

// Config is the "proxy" section of config.json.
type Config struct {
	URL      string            `json:"url"`
	SOCKS    string            `json:"socks,omitempty"`
	NoProxy  []string          `json:"no_proxy,omitempty"`
	Channels map[string]string `json:"channels,omitempty"`
}

// defaultNoProxy keeps loopback and the tailnet off the proxy.
var defaultNoProxy = []string{"127.0.0.1", "localhost", "::1", "100.64.0.0/10", ".ts.net", ".local"}

var envKeys = []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY"}

// Load reads home/config.json. ok is false when there is no usable section.
func Load(home string) (Config, bool, error) {
	data, err := os.ReadFile(filepath.Join(home, "config.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, false, nil
		}
		return Config{}, false, err
	}
	var file struct {
		Proxy *Config `json:"proxy"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return Config{}, false, fmt.Errorf("proxy: config.json: %w", err)
	}
	if file.Proxy == nil {
		return Config{}, false, nil
	}
	if err := file.Proxy.Validate(); err != nil {
		return Config{}, false, err
	}
	return *file.Proxy, true, nil
}

// Validate checks every URL so a typo fails loudly instead of silently
// sending traffic direct.
func (c Config) Validate() error {
	if strings.TrimSpace(c.URL) == "" && strings.TrimSpace(c.SOCKS) == "" {
		return fmt.Errorf("proxy: url or socks is required")
	}
	for _, raw := range []string{c.URL, c.SOCKS} {
		if err := checkURL(raw); err != nil {
			return err
		}
	}
	for ch, v := range c.Channels {
		if v == Direct || v == "" {
			continue
		}
		if err := checkURL(v); err != nil {
			return fmt.Errorf("proxy: channel %s: %w", ch, err)
		}
	}
	return nil
}

func checkURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Port() == "" {
		return fmt.Errorf("proxy: %q needs scheme://host:port", raw)
	}
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h":
		return nil
	}
	return fmt.Errorf("proxy: %q scheme must be http, https, socks5 or socks5h", raw)
}

// Route is the effective outbound path for one channel ("" is the default).
type Route struct {
	Direct  bool
	HTTP    string
	SOCKS   string
	NoProxy string
}

// For resolves the route for a channel.
func (c Config) For(channel string) Route {
	no := c.NoProxy
	if len(no) == 0 {
		no = defaultNoProxy
	}
	r := Route{HTTP: strings.TrimSpace(c.URL), SOCKS: strings.TrimSpace(c.SOCKS), NoProxy: strings.Join(no, ",")}
	switch v := strings.TrimSpace(c.Channels[channel]); {
	case v == Direct:
		return Route{Direct: true}
	case v == "":
	case strings.HasPrefix(v, "socks"):
		r.HTTP, r.SOCKS = "", v
	default:
		r.HTTP, r.SOCKS = v, ""
	}
	return r
}

// Env returns KEY=value pairs, upper and lower case, to append after the
// inherited environment. A direct route sets them all empty.
func (r Route) Env() []string {
	all := r.SOCKS
	if all == "" {
		all = r.HTTP
	}
	vals := map[string]string{"HTTP_PROXY": r.HTTP, "HTTPS_PROXY": r.HTTP, "ALL_PROXY": all, "NO_PROXY": r.NoProxy}
	if r.HTTP == "" && r.SOCKS != "" {
		// Most CLIs only read HTTPS_PROXY; curl, Go and Node accept socks5 there.
		vals["HTTP_PROXY"], vals["HTTPS_PROXY"] = r.SOCKS, r.SOCKS
	}
	if r.Direct {
		vals = map[string]string{"HTTP_PROXY": "", "HTTPS_PROXY": "", "ALL_PROXY": "", "NO_PROXY": ""}
	}
	out := make([]string, 0, 2*len(envKeys))
	for _, k := range envKeys {
		out = append(out, k+"="+vals[k], strings.ToLower(k)+"="+vals[k])
	}
	return out
}

// Describe is a one-line summary for show output and events.
func (r Route) Describe() string {
	if r.Direct {
		return "direct"
	}
	parts := []string{}
	if r.HTTP != "" {
		parts = append(parts, "http "+r.HTTP)
	}
	if r.SOCKS != "" {
		parts = append(parts, "socks "+r.SOCKS)
	}
	return strings.Join(parts, ", ")
}

// ApplyProcess sets the default route on this process, so the serve's own
// git, notify and host children inherit it. It does nothing without a section.
func ApplyProcess(home string) error {
	cfg, ok, err := Load(home)
	if err != nil || !ok {
		return err
	}
	for _, kv := range cfg.For("").Env() {
		k, v, _ := strings.Cut(kv, "=")
		if err := os.Setenv(k, v); err != nil {
			return err
		}
	}
	return nil
}

// ChildEnv is the env to append for a channel's executor, or nil without a
// section. Errors are returned so the caller can record them.
func ChildEnv(home, channel string) ([]string, error) {
	cfg, ok, err := Load(home)
	if err != nil || !ok {
		return nil, err
	}
	return cfg.For(channel).Env(), nil
}

// Check dials each distinct proxy address once.
func (c Config) Check(timeout time.Duration) map[string]error {
	addrs := map[string]bool{}
	for _, raw := range []string{c.URL, c.SOCKS} {
		addrs[raw] = true
	}
	for _, v := range c.Channels {
		addrs[v] = true
	}
	out := map[string]error{}
	keys := make([]string, 0, len(addrs))
	for k := range addrs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, raw := range keys {
		if raw == "" || raw == Direct {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil {
			out[raw] = err
			continue
		}
		conn, err := net.DialTimeout("tcp", u.Host, timeout)
		if err == nil {
			conn.Close()
		}
		out[raw] = err
	}
	return out
}
