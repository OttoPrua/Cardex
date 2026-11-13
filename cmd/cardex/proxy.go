package main

import (
	"context"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/OttoPrua/Cardex/internal/proxy"
)

const proxyGroupUsage = `proxy — the one outbound proxy for the serve and every channel
purpose: show the route each channel uses, or check that the proxy ports answer.
configure it in config.json under "proxy": url, socks, no_proxy, channels{id: direct|url}

parameters:
  show   --json
  check  --json
`

type proxyRoute struct {
	Channel string `json:"channel"`
	Route   string `json:"route"`
}

func cmdProxy(home string, asJSON bool, args []string, stdout, stderr io.Writer) int {
	if groupHelp(stdout, args, proxyGroupUsage) {
		return 0
	}
	dir, err := resolveHome(home)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	cfg, ok, err := proxy.Load(dir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	switch args[0] {
	case "show":
		if !ok {
			if asJSON {
				return writeJSON(stdout, map[string]any{"configured": false})
			}
			fmt.Fprintln(stdout, "no proxy section in config.json; children inherit the serve environment")
			return 0
		}
		routes := []proxyRoute{{Channel: "(default)", Route: cfg.For("").Describe()}}
		ids := map[string]bool{}
		for id := range cfg.Channels {
			ids[id] = true
		}
		if db, err := openStore(home); err == nil {
			if list, err := db.ListChannels(context.Background()); err == nil {
				for _, c := range list {
					ids[c.ID] = true
				}
			}
			db.Close()
		}
		names := make([]string, 0, len(ids))
		for id := range ids {
			names = append(names, id)
		}
		sort.Strings(names)
		for _, id := range names {
			routes = append(routes, proxyRoute{Channel: id, Route: cfg.For(id).Describe()})
		}
		if asJSON {
			return writeJSON(stdout, map[string]any{"configured": true, "no_proxy": cfg.For("").NoProxy, "routes": routes})
		}
		for _, r := range routes {
			fmt.Fprintf(stdout, "%-12s %s\n", r.Channel, r.Route)
		}
		fmt.Fprintf(stdout, "no_proxy     %s\n", cfg.For("").NoProxy)
		return 0
	case "check":
		if !ok {
			fmt.Fprintln(stderr, "no proxy section in config.json")
			return 1
		}
		res := cfg.Check(3 * time.Second)
		keys := make([]string, 0, len(res))
		for k := range res {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		code := 0
		out := map[string]string{}
		for _, k := range keys {
			status := "ok"
			if res[k] != nil {
				status = res[k].Error()
				code = 1
			}
			out[k] = status
			if !asJSON {
				fmt.Fprintf(stdout, "%s  %s\n", k, status)
			}
		}
		if asJSON {
			writeJSON(stdout, out)
		}
		return code
	default:
		fmt.Fprintf(stderr, "unknown proxy verb %q\n", args[0])
		return 2
	}
}
