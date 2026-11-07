package main

// Direct connections (docs/COOP-RELAY.md "Direct connections (R8)"): a host whose game port can be reached from outside (UPnP on its
// router, or a port opened by hand) is paired straight with its partners; the relay only hands out the address and carries the rest
// when that fails. Never for a public battle: the address is the host's home IP, given only to people who have the code.

import (
	"bufio"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"time"
)

var directDial = 2 * time.Second

// the host said "DIRECT <port>": connect back to the address the control link came from (never one the host names: the relay must not be
// a way to make it connect anywhere), say "OGR1 REACH", expect "REACHED". The answer goes back on the control link.
func (s *session) checkDirect(ip, port string) {
	n, err := strconv.Atoi(port)
	if err != nil || n < 1024 || n > 65535 {
		s.direct.Store("")
		s.send("DIRECT NO")
		return
	}
	addr := net.JoinHostPort(ip, strconv.Itoa(n))
	ok := reach(addr)
	if ok {
		s.direct.Store(addr)
		s.send("DIRECT OK")
	} else {
		s.direct.Store("")
		s.send("DIRECT NO")
	}
	log.Printf("direct code=%s addr=%s reachable=%v", s.code, addr, ok)
}

func reach(addr string) bool {
	c, err := net.DialTimeout("tcp", addr, directDial)
	if err != nil {
		return false
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(directDial))
	if _, err := io.WriteString(c, "OGR1 REACH\n"); err != nil {
		return false
	}
	l, err := bufio.NewReader(c).ReadString('\n')
	return err == nil && strings.TrimSpace(l) == "REACHED"
}

func (s *session) directAddr() string {
	s.mu.Lock()
	pub := s.public
	s.mu.Unlock()
	a, _ := s.direct.Load().(string)
	if pub {
		return ""
	}
	return a
}

// a partner that can go direct: give it the address and wait for "RELAY" (it could not connect: carry on through the relay). Anything
// else, or the link closing, means it is with the host now. Returns true to go on with the relayed join.
func (s *session) offerDirect(c net.Conn, rd *bufio.Reader) bool {
	a := s.directAddr()
	if a == "" {
		return true
	}
	io.WriteString(c, "DIRECT "+a+"\n")
	c.SetReadDeadline(time.Now().Add(30 * time.Second))
	l, err := rd.ReadString('\n')
	c.SetReadDeadline(time.Time{})
	if err == nil && strings.TrimSpace(l) == "RELAY" {
		log.Printf("direct-fallback code=%s from=%s", s.code, host(c))
		return true
	}
	log.Printf("direct-join code=%s from=%s", s.code, host(c))
	c.Close()
	return false
}
