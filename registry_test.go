package main

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// a fake community relay: answers PROBE with its own id
func fakeRelay(t *testing.T, id string) (string, int) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			bufio.NewReader(c).ReadString('\n')
			fmt.Fprintf(c, "PROBE %s\n", id)
			c.Close()
		}
	}()
	a := ln.Addr().(*net.TCPAddr)
	return "127.0.0.1", a.Port
}

// the registry: a live relay served, a dead one, a liar, a bad id, an official id, a duplicate and a disabled one left out
func TestRegistry(t *testing.T) {
	old, oldURL := *relays, *registryURL
	*relays = "EU=188.245.71.48:47160"
	defer func() { *relays = old; *registryURL = oldURL; community = nil }()
	h, p := fakeRelay(t, "XFR1")
	_, pl := fakeRelay(t, "OTHER")
	dead, _ := net.Listen("tcp", "127.0.0.1:0")
	dp := dead.Addr().(*net.TCPAddr).Port
	dead.Close()
	js := fmt.Sprintf(`{"relays":[
		{"id":"xfr1","host":"%s","port":%d,"owner":"a"},
		{"id":"DEAD1","host":"127.0.0.1","port":%d},
		{"id":"LIAR","host":"127.0.0.1","port":%d},
		{"id":"BAD-ID","host":"127.0.0.1","port":1},
		{"id":"EU","host":"127.0.0.1","port":1},
		{"id":"XFR1","host":"127.0.0.1","port":2},
		{"id":"OFF1","host":"127.0.0.1","port":%d,"status":"disabled"}]}`, h, p, dp, pl, p)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(js)) }))
	defer srv.Close()
	es, why := parseRegistry([]byte(js))
	if len(es) != 3 || len(why) != 3 {
		t.Fatalf("parsed %d kept, %d skipped: %v", len(es), len(why), why)
	}
	*registryURL = srv.URL
	sweepRegistry()
	a := start(t)
	c, r := dial(t, a, "OGR1 RELAYS")
	defer c.Close()
	want := []string{"RELAY EU 188.245.71.48:47160", fmt.Sprintf("RELAY XFR1 %s:%d COMMUNITY", h, p), "END"}
	for _, w := range want {
		if l := readLine(t, r); l != w {
			t.Fatalf("got %q, want %q", l, w)
		}
	}
	_ = strings.TrimSpace
}

// (R7) a served relay that stops: still served after 1 missed probe, gone after 2, back at its first answer
func TestDownAfterTwoMisses(t *testing.T) {
	old := *relays
	*relays = "EU=188.245.71.48:47160"
	defer func() { *relays = old; listed = nil; community = nil; misses = map[string]int{} }()
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	serve := func(l net.Listener) {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			bufio.NewReader(c).ReadString('\n')
			fmt.Fprintf(c, "PROBE UP1 3/50\n")
			c.Close()
		}
	}
	go serve(ln)
	listed = []regEntry{{ID: "UP1", Host: "127.0.0.1", Port: port}}
	served := func() int { communityMu.Lock(); defer communityMu.Unlock(); return len(community) }
	probeAll()
	if served() != 1 {
		t.Fatal("a live relay not served")
	}
	ln.Close()
	probeAll()
	if served() != 1 {
		t.Fatal("dropped after one miss")
	}
	probeAll()
	if served() != 0 {
		t.Fatal("still served after two misses")
	}
	ln2, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Skip("port taken again")
	}
	defer ln2.Close()
	go serve(ln2)
	probeAll()
	if served() != 1 {
		t.Fatal("not back after answering")
	}
}

// (R7) the cap: -max-sessions wins; else the machine's limits, -max-mbps among them
func TestCap(t *testing.T) {
	oldS, oldM := *maxSessions, *maxMbps
	defer func() { *maxSessions, *maxMbps = oldS, oldM; setCap() }()
	*maxSessions = 3
	setCap()
	if sessionCap != 3 || full(2) || !full(3) {
		t.Fatalf("cap %d", sessionCap)
	}
	*maxSessions, *maxMbps = 0, 2
	setCap()
	if sessionCap != 20 {
		t.Fatalf("2 Mbit/s should give 20, got %d", sessionCap)
	}
	*maxMbps = 0.1
	setCap()
	if sessionCap != minCap {
		t.Fatalf("floor %d, got %d", minCap, sessionCap)
	}
}
