package main

import (
	"bufio"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
)

// a fake game port: answers "OGR1 REACH" with "REACHED"
func gamePort(t *testing.T) int {
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
			go func(c net.Conn) {
				defer c.Close()
				if l, _ := bufio.NewReader(c).ReadString('\n'); strings.TrimSpace(l) == "OGR1 REACH" {
					io.WriteString(c, "REACHED\n")
				}
			}(c)
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

func TestDirect(t *testing.T) {
	a := start(t)
	hc, hr := dial(t, a, "OGR1 HOST 420")
	defer hc.Close()
	code := strings.Fields(readLine(t, hr))[1]
	io.WriteString(hc, "DIRECT "+strconv.Itoa(gamePort(t))+"\n")
	if l := readLine(t, hr); l != "DIRECT OK" {
		t.Fatalf("reachable port: %q", l)
	}
	// an old game (no D): the relay as before
	oc, or := dial(t, a, "OGR1 JOIN "+code+" 420")
	if l := readLine(t, or); l != "WAIT" {
		t.Fatalf("old joiner: %q", l)
	}
	oc.Close()
	readLine(t, hr) // its PEER
	// a new game: the address first; "RELAY" falls back
	jc, jr := dial(t, a, "OGR1 JOIN "+code+" 420 D")
	if l := readLine(t, jr); !strings.HasPrefix(l, "DIRECT 127.0.0.1:") {
		t.Fatalf("direct offer: %q", l)
	}
	io.WriteString(jc, "RELAY\n")
	if l := readLine(t, jr); l != "WAIT" {
		t.Fatalf("after RELAY: %q", l)
	}
	if l := readLine(t, hr); !strings.HasPrefix(l, "PEER ") {
		t.Fatalf("host after RELAY: %q", l)
	}
	jc.Close()
	// public: never the address
	io.WriteString(hc, "INFO 1|me|skirmish|0|0|1/4\n")
	io.WriteString(hc, "PING\n")
	readLine(t, hr) // PONG: INFO was read
	pc, pr := dial(t, a, "OGR1 JOIN "+code+" 420 D")
	defer pc.Close()
	if l := readLine(t, pr); l != "WAIT" {
		t.Fatalf("public battle: %q", l)
	}
	// a closed port
	h2, r2 := dial(t, a, "OGR1 HOST 420")
	defer h2.Close()
	readLine(t, r2)
	io.WriteString(h2, "DIRECT 1\n")
	if l := readLine(t, r2); l != "DIRECT NO" {
		t.Fatalf("bad port: %q", l)
	}
}
