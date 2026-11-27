package runner

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"time"
)

type compatibleLookup func(context.Context, string, string) ([]netip.Addr, error)
type compatibleDial func(context.Context, string, string) (net.Conn, error)

var neverPublicIPv4 = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),       // current network
	netip.MustParsePrefix("100.64.0.0/10"),   // shared address space
	netip.MustParsePrefix("192.0.0.0/24"),    // protocol assignments
	netip.MustParsePrefix("192.0.2.0/24"),    // documentation
	netip.MustParsePrefix("192.88.99.0/24"),  // deprecated relay anycast
	netip.MustParsePrefix("198.18.0.0/15"),   // benchmark
	netip.MustParsePrefix("198.51.100.0/24"), // documentation
	netip.MustParsePrefix("203.0.113.0/24"),  // documentation
	netip.MustParsePrefix("240.0.0.0/4"),     // reserved
}

var publicIPv6Space = netip.MustParsePrefix("2000::/3")
var neverPublicIPv6 = []netip.Prefix{
	netip.MustParsePrefix("2001::/23"),     // transition and protocol assignments
	netip.MustParsePrefix("2001:db8::/32"), // documentation
	netip.MustParsePrefix("2002::/16"),     // 6to4 (embedded v4)
	netip.MustParsePrefix("3fff::/20"),     // documentation
}

func compatiblePublicIP(a netip.Addr) bool {
	if a.Zone() != "" {
		return false
	}
	a = a.Unmap()
	if !a.IsGlobalUnicast() || a.IsPrivate() || a.IsLinkLocalUnicast() {
		return false
	}
	blocked := neverPublicIPv4
	if !a.Is4() {
		if !publicIPv6Space.Contains(a) {
			return false
		}
		blocked = neverPublicIPv6
	}
	for _, p := range blocked {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

// compatibleTargetDial resolves, checks and dials the validated address itself
// (never the hostname again). HTTP still verifies TLS against the original
// hostname. A mixed public/private result fails closed rather than selecting
// an address from an untrusted answer.
func compatibleTargetDial(host, targetPort string, lookup compatibleLookup, dial compatibleDial) compatibleDial {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		requestHost, port, err := net.SplitHostPort(address)
		if err != nil || !strings.EqualFold(requestHost, host) || port != targetPort || network != "tcp" {
			return nil, errors.New("api channel connection does not match its configured origin")
		}
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		ips, err := lookup(ctx, "ip", host)
		if err != nil || len(ips) == 0 {
			return nil, errors.New("api channel target could not be resolved")
		}
		for _, ip := range ips {
			if !compatiblePublicIP(ip) {
				return nil, errors.New("api channel target resolved to a non-public address")
			}
		}
		for _, ip := range ips {
			if c, err := dial(ctx, network, net.JoinHostPort(ip.Unmap().String(), port)); err == nil {
				return c, nil
			}
			if ctx.Err() != nil {
				break
			}
		}
		return nil, errors.New("api channel could not connect to its verified target")
	}
}
