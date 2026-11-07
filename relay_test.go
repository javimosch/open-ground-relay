package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func start(t *testing.T) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r := newRelay()
	go r.serve(ln)
	t.Cleanup(func() { ln.Close() })
	return ln.Addr().String()
}

func dial(t *testing.T, a, first string) (net.Conn, *bufio.Reader) {
	c, err := net.Dial("tcp", a)
	if err != nil {
		t.Fatal(err)
	}
	io.WriteString(c, first+"\n")
	return c, bufio.NewReader(c)
}

func readLine(t *testing.T, r *bufio.Reader) string {
	l, err := r.ReadString('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return strings.TrimSpace(l)
}

// a host, a partner, the pairing, 5 MB each way intact; then a rejoin on the same code
func TestPairAndPipe(t *testing.T) {
	a := start(t)
	hc, hr := dial(t, a, "OGR1 HOST 420")
	defer hc.Close()
	l := readLine(t, hr)
	if !strings.HasPrefix(l, "CODE EU-") {
		t.Fatalf("got %q", l)
	}
	code := strings.Fields(l)[1]
	for round := 0; round < 2; round++ {
		jc, jr := dial(t, a, "OGR1 JOIN "+strings.ToLower(code)+" 420")
		if w := readLine(t, jr); w != "WAIT" {
			t.Fatalf("got %q", w)
		}
		peer := readLine(t, hr)
		if !strings.HasPrefix(peer, "PEER ") {
			t.Fatalf("got %q", peer)
		}
		dc, dr := dial(t, a, "OGR1 ACCEPT "+strings.Fields(peer)[1])
		if ok := readLine(t, dr); ok != "OK" {
			t.Fatalf("host data got %q", ok)
		}
		if ok := readLine(t, jr); ok != "OK" {
			t.Fatalf("join got %q", ok)
		}
		up, down := make([]byte, 5<<20), make([]byte, 5<<20)
		rand.Read(up)
		rand.Read(down)
		go dc.Write(up)
		go jc.Write(down)
		gotJ, gotH := make([]byte, len(up)), make([]byte, len(down))
		jc.SetReadDeadline(time.Now().Add(10 * time.Second))
		dc.SetReadDeadline(time.Now().Add(10 * time.Second))
		if _, err := io.ReadFull(jr, gotJ); err != nil || !bytes.Equal(gotJ, up) {
			t.Fatalf("host->join: %v", err)
		}
		if _, err := io.ReadFull(dr, gotH); err != nil || !bytes.Equal(gotH, down) {
			t.Fatalf("join->host: %v", err)
		}
		jc.Close() // the partner leaves: the host's data link closes too
		dc.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, err := dr.ReadByte(); err == nil {
			t.Fatal("host data link still open after the partner left")
		}
		dc.Close()
	}
	io.WriteString(hc, "PING\n")
	if p := readLine(t, hr); p != "PONG" {
		t.Fatalf("got %q", p)
	}
}

func TestErrors(t *testing.T) {
	a := start(t)
	jc, jr := dial(t, a, "OGR1 JOIN EU-NOPE-99 420")
	defer jc.Close()
	if l := readLine(t, jr); !strings.HasPrefix(l, "ERR no battle") {
		t.Fatalf("got %q", l)
	}
	hc, hr := dial(t, a, "OGR1 HOST 420")
	code := strings.Fields(readLine(t, hr))[1]
	vc, vr := dial(t, a, "OGR1 JOIN "+code+" 419")
	defer vc.Close()
	if l := readLine(t, vr); !strings.Contains(l, "different versions") {
		t.Fatalf("got %q", l)
	}
	pc, pr := dial(t, a, "OGR1 PROBE")
	defer pc.Close()
	if l := readLine(t, pr); !strings.HasPrefix(l, "PROBE EU ") || !strings.Contains(l, "/") {
		t.Fatalf("got %q", l)
	}
	// the host leaves: its code dies
	hc.Close()
	time.Sleep(200 * time.Millisecond)
	gc, gr := dial(t, a, "OGR1 JOIN "+code+" 420")
	defer gc.Close()
	if l := readLine(t, gr); !strings.HasPrefix(l, "ERR no battle") {
		t.Fatalf("after the host left got %q", l)
	}
}

func TestRateLimit(t *testing.T) {
	a := start(t)
	limited := false
	for i := 0; i < *joinPerMin+2; i++ {
		c, r := dial(t, a, "OGR1 JOIN EU-X-22 420")
		if strings.HasPrefix(readLine(t, r), "ERR too many") {
			limited = true
		}
		c.Close()
	}
	if !limited {
		t.Fatal("no rate limit")
	}
}

// the public list: only INFO-public battles of the same version, not one with its partner in
func TestList(t *testing.T) {
	a := start(t)
	h1, r1 := dial(t, a, "OGR1 HOST 420")
	defer h1.Close()
	c1 := strings.Fields(readLine(t, r1))[1]
	io.WriteString(h1, "INFO 1|Javi|Hold the bridge|0|1\n")
	h2, r2 := dial(t, a, "OGR1 HOST 420")
	defer h2.Close()
	readLine(t, r2)
	io.WriteString(h2, "INFO 0|Private|x|0|0\n") // code only: not listed
	h3, r3 := dial(t, a, "OGR1 HOST 419")
	defer h3.Close()
	readLine(t, r3)
	io.WriteString(h3, "INFO 1|Old|x|0|0\n") // another version
	time.Sleep(100 * time.Millisecond)
	lc, lr := dial(t, a, "OGR1 LIST 420")
	defer lc.Close()
	var got []string
	for {
		l := readLine(t, lr)
		if l == "END" {
			break
		}
		got = append(got, l)
	}
	if len(got) != 1 || got[0] != "BATTLE "+c1+"|Javi|Hold the bridge|0|1" {
		t.Fatalf("list %q", got)
	}
	// players/max: listed while there is room, not when full
	io.WriteString(h1, "INFO 1|Javi|Hold the bridge|0|1|3/4\n")
	time.Sleep(100 * time.Millisecond)
	if n := listCount(t, a); n != 1 {
		t.Fatalf("3/4 listed %d", n)
	}
	io.WriteString(h1, "INFO 1|Javi|Hold the bridge|0|1|4/4\n")
	time.Sleep(100 * time.Millisecond)
	if n := listCount(t, a); n != 0 {
		t.Fatalf("4/4 listed %d", n)
	}
}

func listCount(t *testing.T, a string) int {
	c, r := dial(t, a, "OGR1 LIST 420")
	defer c.Close()
	n := 0
	for {
		l := readLine(t, r)
		if l == "END" {
			return n
		}
		n++
	}
}

func TestRelays(t *testing.T) {
	old := *relays
	*relays = "EU=188.245.71.48:47160, us=relay-us.example:47160"
	defer func() { *relays = old }()
	a := start(t)
	c, r := dial(t, a, "OGR1 RELAYS")
	defer c.Close()
	if l := readLine(t, r); l != "RELAY EU 188.245.71.48:47160" {
		t.Fatalf("got %q", l)
	}
	if l := readLine(t, r); l != "RELAY US relay-us.example:47160" {
		t.Fatalf("got %q", l)
	}
	if l := readLine(t, r); l != "END" {
		t.Fatalf("got %q", l)
	}
}
