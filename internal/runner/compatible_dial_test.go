package runner

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
)

func TestCompatibleTransportNeverDialsUnapprovedDNSAnswers(t *testing.T) {
	for _, ip := range []string{
		"0.1.2.3", "127.0.0.1", "::1", "10.0.0.1", "172.16.0.1",
		"::ffff:192.168.20.1", "100.100.0.1", "169.254.169.254", "224.0.0.3",
		"192.0.2.1", "198.18.1.2", "203.0.113.2", "255.255.255.255",
		"fe80::1", "fc00::1", "ff00::1", "::ffff:127.3.0.1", "2002:0a00:0001::",
		"64:ff9b::a00:1", "2001:db8::1", "3fff::1",
	} {
		t.Run(ip, func(t *testing.T) {
			transport := newCompatibleTransport("api.example.test", "", func(context.Context, string, string) ([]netip.Addr, error) {
				return []netip.Addr{netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr(ip)}, nil
			}, func(context.Context, string, string) (net.Conn, error) {
				t.Error("dialed an ambiguous or non-public result")
				return nil, errors.New("test must not dial")
			})
			if _, err := transport.DialContext(context.Background(), "tcp", "api.example.test:443"); err == nil {
				t.Fatal("accepted a DNS response with a private or reserved address")
			}
		})
	}
}

func TestCompatibleTransportPreservesHostTLSAndDialsOnlyPinnedAddresses(t *testing.T) {
	const host = "api.example.test"
	lookups, dials := 0, 0
	transport := newCompatibleTransport(host, "443", func(_ context.Context, network, domain string) ([]netip.Addr, error) {
		lookups++
		if network != "ip" || domain != host {
			t.Fatal("resolved a different origin")
		}
		return []netip.Addr{netip.MustParseAddr("93.184.216.34"),
			netip.MustParseAddr("2606:4700:4700::1111")}, nil
	}, func(ctx context.Context, network, addr string) (net.Conn, error) {
		dials++
		if _, ok := ctx.Deadline(); !ok || network != "tcp" {
			t.Fatal("dial lost its bound timeout or network")
		}
		switch dials {
		case 1:
			if addr != "93.184.216.34:443" {
				t.Fatal("unexpected first dial address")
			}
			return nil, errors.New("mock retry")
		case 2:
			if addr != "[2606:4700:4700::1111]:443" {
				t.Fatal("unexpected second dial address")
			}
		default:
			t.Fatal("unexpected additional dial")
		}
		one, two := net.Pipe()
		_ = two.Close()
		return one, nil
	})
	if transport.Proxy != nil || transport.TLSClientConfig.InsecureSkipVerify || transport.TLSClientConfig.ServerName != "" {
		t.Fatal("transport could use a proxy or ignore the URL hostname at TLS")
	}
	for _, target := range []string{"different.example.test:443", host + ":8443"} {
		if _, err := transport.DialContext(context.Background(), "tcp", target); err == nil || lookups != 0 {
			t.Fatal("origin resolution was not pinned")
		}
	}
	c, err := transport.DialContext(context.Background(), "tcp", host+":443")
	if err != nil || lookups != 1 || dials != 2 {
		t.Fatalf("verified addresses did not pass: %v (lookups %d dials %d)", err, lookups, dials)
	}
	_ = c.Close()
}
