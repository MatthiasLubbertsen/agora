// Package upstream connects the gateway to the MCP servers it fronts.
package upstream

import (
	"fmt"
	"net"
	"net/http"
	"syscall"
	"time"
)

var cgnat = mustCIDR("100.64.0.0/10")

func mustCIDR(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	return n
}

func blockedIP(ip net.IP) bool {
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() || ip.IsMulticast() || cgnat.Contains(ip)
}

// NewHTTPClient returns an HTTP client for talking to upstream servers. Unless
// allowPrivate is set it refuses to connect to loopback, private, link-local
// and similar addresses. The check runs on the resolved address at dial time,
// so DNS rebinding cannot get around it.
func NewHTTPClient(allowPrivate bool) *http.Client {
	d := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	if !allowPrivate {
		d.Control = func(network, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip := net.ParseIP(host)
			if ip == nil || blockedIP(ip) {
				return fmt.Errorf("connections to private or local addresses are blocked (set ALLOW_PRIVATE_UPSTREAMS=true to allow)")
			}
			return nil
		}
	}
	return &http.Client{Transport: &http.Transport{
		DialContext:           d.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
	}}
}
