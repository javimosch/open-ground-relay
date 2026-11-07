// og-relay: the Open Ground co-op relay (docs/COOP-RELAY.md). Both games connect OUT to it; it pairs them by a join code and pipes the
// bytes. It never reads the game's lines after the pairing and never takes part in the lockstep.
//
//	host control link:  "OGR1 HOST <sim version>"         -> "CODE <code> <ttl s>"; then PING / PONG; the relay sends "PEER <token>"
//	partner:            "OGR1 JOIN <code> <sim version>"  -> "WAIT" (the host is told) or "ERR <why>"
//	host data link:     "OGR1 ACCEPT <token>"             -> "OK" to both, then byte for byte both ways
//	anyone:             "OGR1 PROBE"                       -> "PROBE <region> <battles>/<cap>" (the game measures relays and their room)
//	host control link:  "INFO <public 0/1>|name|battle|shared 0/1|password 0/1|players/max" (any time: what the public list shows)
//	anyone:             "OGR1 LIST <sim version>"         -> "BATTLE code|name|battle|shared|password" per public, not full battle; "END"
//	anyone:             "OGR1 RELAYS"                      -> "RELAY <region> <host:port> [COMMUNITY]" per relay in -relays, then the live
//	                                                         community ones from -registry (registry.go), "END"
//	127.0.0.1 only:     "OGR1 STATUS"                      -> the live sessions, one per line, then "END"
package main

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var (
	addr        = flag.String("addr", ":47160", "listen address")
	region      = flag.String("region", "EU", "this relay's region id (the code's prefix)")
	maxSessions = flag.Int("max-sessions", 0, "open battles at most; 0: worked out from the machine and -max-mbps (capacity.go)")
	maxBytes    = flag.Int64("max-bytes", 500<<20, "bytes at most per piped link (both ways)")
	joinPerMin  = flag.Int("join-per-min", 10, "join attempts a minute per address")
	relays      = flag.String("relays", "", "every relay the games may use, REGION=host:port,... (served by RELAYS; this one included)")
)

// codes: a word and two digits, read out loud easily (no O/0, I/1 confusion: the digits are 2-9)
var words = strings.Fields(`ALPHA BRAVO DELTA ECHO FOXTROT GOLF HOTEL KILO LIMA MIKE NOVEMBER OSCAR PAPA ROMEO SIERRA TANGO UNIFORM
VICTOR WHISKEY ZULU AMBER BADGER BISON CANYON CEDAR COBALT CONDOR CORAL CRANE DUNE EAGLE EMBER FALCON FERN FJORD FROST GRANITE HARBOR
HAWK HERON IRON JACKAL JADE JUNIPER KESTREL LANCE LANTERN LYNX MAPLE MARBLE MESA MOOSE NORTH OAK ONYX OTTER PANTHER PINE PLATEAU QUARTZ
RAVEN REEF RIDGE RIVER SABLE SAGE SPARROW SPRUCE STONE SUMMIT TIGER TIMBER TUNDRA VALLEY VIPER WALNUT WILLOW WOLF`)

type session struct {
	code    string
	ver     string
	ctrl    net.Conn
	ctrlMu  sync.Mutex
	started time.Time
	joins   int64
	pending map[string]*pendingJoin // token -> the partner waiting for the host's ACCEPT
	mu      sync.Mutex
	public  bool   // (INFO) listed for anyone of the same version
	info    string // name|battle|shared|password, as the host sent it (cleaned)
	paired  int32  // partners piped right now
	full    int    // (INFO players/max) 1: full, 0: room, -1: not said (then a battle with anyone paired is not listed)
}

type pendingJoin struct {
	conn net.Conn
	rd   *bufio.Reader
	done chan struct{}
}

type relay struct {
	mu       sync.Mutex
	sessions map[string]*session
	tokens   map[string]*session
	joins    map[string][]time.Time // address -> recent join attempts
	piped    int64
}

func newRelay() *relay {
	setCap()
	return &relay{sessions: map[string]*session{}, tokens: map[string]*session{}, joins: map[string][]time.Time{}}
}

func randInt(n int) int {
	v, _ := rand.Int(rand.Reader, big.NewInt(int64(n)))
	return int(v.Int64())
}

func (r *relay) newCode() string {
	for {
		c := fmt.Sprintf("%s-%s-%d%d", *region, words[randInt(len(words))], 2+randInt(8), 2+randInt(8))
		if _, taken := r.sessions[c]; !taken {
			return c
		}
	}
}

func token() string {
	b := make([]byte, 12)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func host(c net.Conn) string {
	h, _, err := net.SplitHostPort(c.RemoteAddr().String())
	if err != nil {
		return c.RemoteAddr().String()
	}
	return h
}

func (s *session) send(line string) error {
	s.ctrlMu.Lock()
	defer s.ctrlMu.Unlock()
	s.ctrl.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_, err := io.WriteString(s.ctrl, line+"\n")
	return err
}

// rate limit: joinPerMin attempts a minute per address (codes can't be guessed)
func (r *relay) allowJoin(a string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	keep := r.joins[a][:0]
	for _, t := range r.joins[a] {
		if now.Sub(t) < time.Minute {
			keep = append(keep, t)
		}
	}
	if len(keep) >= *joinPerMin {
		r.joins[a] = keep
		return false
	}
	r.joins[a] = append(keep, now)
	return true
}

func (r *relay) handle(c net.Conn) {
	rd := bufio.NewReaderSize(c, 4096)
	c.SetReadDeadline(time.Now().Add(15 * time.Second))
	line, err := rd.ReadString('\n')
	if err != nil || len(line) > 300 {
		c.Close()
		return
	}
	f := strings.Fields(line)
	if len(f) < 2 || f[0] != "OGR1" {
		c.Close()
		return
	}
	c.SetReadDeadline(time.Time{})
	switch f[1] {
	case "PROBE":
		r.mu.Lock()
		n := len(r.sessions)
		r.mu.Unlock()
		used := n
		if full(n) && used < sessionCap {
			used = sessionCap // (the bandwidth is used up: say full)
		}
		fmt.Fprintf(c, "PROBE %s %d/%d\n", *region, used, sessionCap)
		c.Close()
	case "STATUS":
		r.status(c)
	case "RELAYS":
		writeRelays(c)
		c.Close()
	case "LIST":
		if len(f) < 3 {
			c.Close()
			return
		}
		r.list(c, f[2])
	case "HOST":
		if len(f) < 3 {
			c.Close()
			return
		}
		r.hostLink(c, rd, f[2])
	case "JOIN":
		if len(f) < 4 {
			c.Close()
			return
		}
		r.joinLink(c, rd, strings.ToUpper(f[2]), f[3])
	case "ACCEPT":
		if len(f) < 3 {
			c.Close()
			return
		}
		r.acceptLink(c, rd, f[2])
	default:
		c.Close()
	}
}

func (r *relay) status(c net.Conn) {
	defer c.Close()
	if h := host(c); h != "127.0.0.1" && h != "::1" {
		return
	}
	r.mu.Lock()
	fmt.Fprintf(c, "region %s sessions %d piped %d MB\n", *region, len(r.sessions), atomic.LoadInt64(&r.piped)>>20)
	for _, s := range r.sessions {
		fmt.Fprintf(c, "%s ver %s host %s up %s joins %d\n", s.code, s.ver, host(s.ctrl), time.Since(s.started).Round(time.Second), atomic.LoadInt64(&s.joins))
	}
	r.mu.Unlock()
	io.WriteString(c, "END\n")
}

// INFO "<public>|name|battle|shared|password": fields cleaned (printable, short) - strangers read them in the public list
func (s *session) setInfo(v string) {
	// "<public>|name|battle|shared|password|players/max"
	p := strings.Split(v, "|")
	for len(p) < 6 {
		p = append(p, "")
	}
	clean := func(x string, n int) string {
		b := []rune{}
		for _, ch := range x {
			if ch >= 32 && ch != 127 && ch != '|' {
				b = append(b, ch)
			}
			if len(b) >= n {
				break
			}
		}
		return strings.TrimSpace(string(b))
	}
	s.mu.Lock()
	s.full = -1
	if a, b, ok := strings.Cut(p[5], "/"); ok {
		var n, m int
		fmt.Sscan(a, &n)
		fmt.Sscan(b, &m)
		if m > 0 {
			s.full = 0
			if n >= m {
				s.full = 1
			}
		}
	}
	s.public = p[0] == "1"
	s.info = clean(p[1], 24) + "|" + clean(p[2], 40) + "|" + clean(p[3], 1) + "|" + clean(p[4], 1)
	s.mu.Unlock()
}

func (r *relay) list(c net.Conn, ver string) {
	defer c.Close()
	if !r.allowJoin(host(c)) { // (the same budget as joins: the list is not a scanner)
		io.WriteString(c, "ERR too many tries, wait a minute\nEND\n")
		return
	}
	r.mu.Lock()
	var out []string
	for _, s := range r.sessions {
		s.mu.Lock()
		listed := s.full == 0 || (s.full < 0 && atomic.LoadInt32(&s.paired) == 0)
		if s.public && s.ver == ver && listed {
			out = append(out, "BATTLE "+s.code+"|"+s.info)
		}
		s.mu.Unlock()
		if len(out) >= 100 {
			break
		}
	}
	r.mu.Unlock()
	for _, l := range out {
		io.WriteString(c, l+"\n")
	}
	io.WriteString(c, "END\n")
}

func (r *relay) hostLink(c net.Conn, rd *bufio.Reader, ver string) {
	r.mu.Lock()
	if full(len(r.sessions)) {
		r.mu.Unlock()
		io.WriteString(c, "ERR the relay is full, try again later\n")
		c.Close()
		log.Printf("host-refused full from=%s", host(c))
		return
	}
	s := &session{code: r.newCode(), ver: ver, ctrl: c, started: time.Now(), pending: map[string]*pendingJoin{}}
	r.sessions[s.code] = s
	r.mu.Unlock()
	log.Printf("open code=%s ver=%s from=%s", s.code, ver, host(c))
	s.send("CODE " + s.code + " 0")
	// the control link: PING / PONG; reading stops when it closes (then the code dies)
	for {
		c.SetReadDeadline(time.Now().Add(90 * time.Second))
		l, err := rd.ReadString('\n')
		if err != nil {
			break
		}
		if strings.HasPrefix(l, "PING") {
			s.send("PONG")
		} else if strings.HasPrefix(l, "INFO ") {
			s.setInfo(strings.TrimSpace(l[5:]))
		}
	}
	c.Close()
	r.mu.Lock()
	delete(r.sessions, s.code)
	s.mu.Lock()
	for t, p := range s.pending {
		delete(r.tokens, t)
		io.WriteString(p.conn, "ERR the host left\n")
		p.conn.Close()
		close(p.done)
	}
	s.pending = map[string]*pendingJoin{}
	s.mu.Unlock()
	r.mu.Unlock()
	log.Printf("close code=%s up=%s joins=%d", s.code, time.Since(s.started).Round(time.Second), atomic.LoadInt64(&s.joins))
}

func (r *relay) joinLink(c net.Conn, rd *bufio.Reader, code, ver string) {
	a := host(c)
	if !r.allowJoin(a) {
		io.WriteString(c, "ERR too many tries, wait a minute\n")
		c.Close()
		log.Printf("join-limited from=%s", a)
		return
	}
	r.mu.Lock()
	s := r.sessions[code]
	r.mu.Unlock()
	if s == nil {
		io.WriteString(c, "ERR no battle with that code (it ended, or a typo)\n")
		c.Close()
		log.Printf("join-nocode code=%s from=%s", code, a)
		return
	}
	if ver != s.ver {
		io.WriteString(c, "ERR the two games are different versions ("+s.ver+" and "+ver+"): update both to the same build\n")
		c.Close()
		log.Printf("join-version code=%s host=%s join=%s from=%s", code, s.ver, ver, a)
		return
	}
	t := token()
	p := &pendingJoin{conn: c, rd: rd, done: make(chan struct{})}
	r.mu.Lock()
	s.mu.Lock()
	s.pending[t] = p
	s.mu.Unlock()
	r.tokens[t] = s
	r.mu.Unlock()
	io.WriteString(c, "WAIT\n")
	if s.send("PEER "+t) != nil {
		r.dropPending(s, t, "ERR the host is not answering")
		return
	}
	atomic.AddInt64(&s.joins, 1)
	log.Printf("join code=%s from=%s", code, a)
	select { // the host's ACCEPT closes done (and pipes); else give up
	case <-p.done:
	case <-time.After(20 * time.Second):
		r.dropPending(s, t, "ERR the host did not answer")
	}
}

func (r *relay) dropPending(s *session, t, why string) {
	r.mu.Lock()
	delete(r.tokens, t)
	r.mu.Unlock()
	s.mu.Lock()
	p := s.pending[t]
	delete(s.pending, t)
	s.mu.Unlock()
	if p != nil {
		io.WriteString(p.conn, why+"\n")
		p.conn.Close()
		select {
		case <-p.done:
		default:
			close(p.done)
		}
	}
}

func (r *relay) acceptLink(c net.Conn, rd *bufio.Reader, t string) {
	r.mu.Lock()
	s := r.tokens[t]
	delete(r.tokens, t)
	r.mu.Unlock()
	if s == nil {
		io.WriteString(c, "ERR no partner waiting\n")
		c.Close()
		return
	}
	s.mu.Lock()
	p := s.pending[t]
	delete(s.pending, t)
	s.mu.Unlock()
	if p == nil {
		c.Close()
		return
	}
	io.WriteString(c, "OK\n")
	io.WriteString(p.conn, "OK\n")
	log.Printf("paired code=%s", s.code)
	start := time.Now()
	atomic.AddInt32(&s.paired, 1)
	n := pipe(c, rd, p.conn, p.rd)
	atomic.AddInt32(&s.paired, -1)
	atomic.AddInt64(&r.piped, n)
	atomic.AddInt64(&pipedTotal, n)
	log.Printf("unpaired code=%s bytes=%d up=%s", s.code, n, time.Since(start).Round(time.Second))
	close(p.done)
}

// both ways until either side closes (or the byte cap); the readers may already hold bytes past the first line
func pipe(a net.Conn, ar *bufio.Reader, b net.Conn, br *bufio.Reader) int64 {
	var total int64
	var wg sync.WaitGroup
	cp := func(dst net.Conn, src *bufio.Reader) {
		defer wg.Done()
		n, _ := io.Copy(dst, io.LimitReader(src, *maxBytes))
		atomic.AddInt64(&total, n)
		a.Close()
		b.Close()
	}
	wg.Add(2)
	go cp(b, ar)
	go cp(a, br)
	wg.Wait()
	return total
}

func (r *relay) serve(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		if tc, ok := c.(*net.TCPConn); ok {
			tc.SetNoDelay(true)
			tc.SetKeepAlive(true)
			tc.SetKeepAlivePeriod(30 * time.Second)
		}
		go r.handle(c)
	}
}

func main() {
	status := flag.Bool("status", false, "print the live sessions of the relay on -addr and exit")
	flag.Parse()
	log.SetFlags(log.LstdFlags | log.LUTC)
	if *status {
		a := *addr
		if strings.HasPrefix(a, ":") {
			a = "127.0.0.1" + a
		}
		c, err := net.DialTimeout("tcp", a, 3*time.Second)
		if err != nil {
			fmt.Fprintln(os.Stderr, "og-relay: not running on", a, ":", err)
			os.Exit(1)
		}
		io.WriteString(c, "OGR1 STATUS\n")
		io.Copy(os.Stdout, c)
		return
	}
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	go watchRate()
	log.Printf("og-relay region=%s on %s", *region, *addr)
	go watchRegistry()
	newRelay().serve(ln)
}
