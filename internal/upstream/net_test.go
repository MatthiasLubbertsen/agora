package upstream

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBlockedIP(t *testing.T) {
	for ip, want := range map[string]bool{
		"127.0.0.1": true, "::1": true, "10.1.2.3": true, "192.168.0.5": true, "172.16.0.1": true,
		"169.254.169.254": true, "100.64.0.1": true, "0.0.0.0": true, "fd00::1": true, "::ffff:127.0.0.1": true,
		"8.8.8.8": false, "1.1.1.1": false, "2606:4700::1111": false,
	} {
		if got := blockedIP(net.ParseIP(ip)); got != want {
			t.Errorf("blockedIP(%s) = %v, want %v", ip, got, want)
		}
	}
}

func TestClientRefusesLoopbackUnlessAllowed(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("hi")) }))
	defer ts.Close()
	if resp, err := NewHTTPClient(false).Get(ts.URL); err == nil {
		resp.Body.Close()
		t.Fatal("loopback request succeeded with the guard on")
	}
	resp, err := NewHTTPClient(true).Get(ts.URL)
	if err != nil {
		t.Fatalf("loopback request failed with the guard off: %v", err)
	}
	resp.Body.Close()
}
