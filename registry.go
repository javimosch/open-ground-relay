// (R6) community relays: a public registry (relays.json in the open-ground-community repo) lists relays run by players. A relay started
// with -registry URL fetches it every -registry-every, probes each entry ("OGR1 PROBE" must answer "PROBE <its id>") and adds the live
// ones to its RELAYS answer, tagged COMMUNITY. The games need nothing new: they learn the list from any relay (docs/COOP-RELAY.md).
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

var (
	registryURL   = flag.String("registry", "", "community relay registry (relays.json URL); empty: official relays only")
	registryEvery = flag.Duration("registry-every", time.Hour, "how often the registry is fetched")
	probeEvery    = flag.Duration("probe-every", 5*time.Minute, "how often the listed relays are probed (down after 2 misses, back at the first answer)")
)

// an id is the join code's prefix: letters and digits, no dash (the code is ID-WORD-NN)
var relayID = regexp.MustCompile(`^[A-Z][A-Z0-9]{1,5}$`)

type regEntry struct {
	ID      string `json:"id"`
	Host    string `json:"host"`
	Port    int    `json:"port"`
	Owner   string `json:"owner"`
	Contact string `json:"contact"`
	Status  string `json:"status"` // "" or "active": listed; "disabled": kept in the file, not served
}

type registry struct {
	Relays []regEntry `json:"relays"`
}

var (
	communityMu sync.Mutex
	community   []string // "ID host:port", served now
	listed      []regEntry
	misses      = map[string]int{} // id -> probes missed in a row
)

// the ids of -relays (official): a community entry can never take one
func officialIDs() map[string]bool {
	m := map[string]bool{strings.ToUpper(*region): true}
	for _, e := range strings.Split(*relays, ",") {
		if id, _, ok := strings.Cut(strings.TrimSpace(e), "="); ok {
			m[strings.ToUpper(id)] = true
		}
	}
	return m
}

// parseRegistry: the entries a relay may serve, checked (bad id, taken id, duplicate, bad host or port: dropped with a reason)
func parseRegistry(b []byte) ([]regEntry, []string) {
	var r registry
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, []string{"registry: " + err.Error()}
	}
	off := officialIDs()
	seen := map[string]bool{}
	var out []regEntry
	var why []string
	for _, e := range r.Relays {
		e.ID = strings.ToUpper(strings.TrimSpace(e.ID))
		switch {
		case e.Status == "disabled":
			continue
		case !relayID.MatchString(e.ID):
			why = append(why, fmt.Sprintf("%q: bad id (2-6 letters/digits, a letter first)", e.ID))
		case off[e.ID]:
			why = append(why, e.ID+": an official relay's id")
		case seen[e.ID]:
			why = append(why, e.ID+": listed twice")
		case e.Host == "" || strings.ContainsAny(e.Host, " /:,=") || e.Port <= 0 || e.Port > 65535:
			why = append(why, e.ID+": bad host or port")
		default:
			seen[e.ID] = true
			out = append(out, e)
		}
	}
	return out, why
}

// probe: does host:port answer "PROBE <id>" within 4 s
func probe(e regEntry) bool {
	c, err := net.DialTimeout("tcp", net.JoinHostPort(e.Host, fmt.Sprint(e.Port)), 4*time.Second)
	if err != nil {
		return false
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(4 * time.Second))
	io.WriteString(c, "OGR1 PROBE\n")
	l, err := bufio.NewReader(io.LimitReader(c, 256)).ReadString('\n')
	f := strings.Fields(l) // "PROBE <id> [used/cap]"
	return err == nil && len(f) >= 2 && f[0] == "PROBE" && f[1] == e.ID
}

func sweepRegistry() {
	cl := &http.Client{Timeout: 20 * time.Second}
	resp, err := cl.Get(*registryURL)
	if err != nil {
		log.Printf("registry: %v (the last list stays)", err)
		return
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || resp.StatusCode != 200 {
		log.Printf("registry: http %d %v (the last list stays)", resp.StatusCode, err)
		return
	}
	es, why := parseRegistry(b)
	for _, w := range why {
		log.Printf("registry: skipped %s", w)
	}
	communityMu.Lock()
	listed = es
	communityMu.Unlock()
	probeAll()
}

// probe every listed relay; one missing 2 probes in a row leaves the RELAYS answer until it answers again
func probeAll() {
	communityMu.Lock()
	es := append([]regEntry(nil), listed...)
	communityMu.Unlock()
	ok := make([]bool, len(es))
	var wg sync.WaitGroup
	for i, e := range es {
		wg.Add(1)
		go func(i int, e regEntry) { defer wg.Done(); ok[i] = probe(e) }(i, e)
	}
	wg.Wait()
	var live []string
	communityMu.Lock()
	for i, e := range es {
		was, known := misses[e.ID]
		if !known {
			was = 2 // (a new entry is served once it has answered)
			misses[e.ID] = 2
		}
		if ok[i] {
			misses[e.ID] = 0
			if known && was >= 2 {
				log.Printf("registry: %s answers again", e.ID)
			}
		} else {
			misses[e.ID]++
			if known && misses[e.ID] == 2 {
				log.Printf("registry: %s (%s:%d) missed 2 probes: down, not served", e.ID, e.Host, e.Port)
			}
		}
		if misses[e.ID] < 2 {
			live = append(live, e.ID+" "+net.JoinHostPort(e.Host, fmt.Sprint(e.Port)))
		}
	}
	community = live
	communityMu.Unlock()
	log.Printf("registry: %d community relays served of %d listed", len(live), len(es))
}

func watchRegistry() {
	if *registryURL == "" {
		return
	}
	sweepRegistry()
	fetch, pr := time.NewTicker(*registryEvery), time.NewTicker(*probeEvery)
	for {
		select {
		case <-fetch.C:
			sweepRegistry()
		case <-pr.C:
			probeAll()
		}
	}
}

// the RELAYS answer: the official ones, then the live community ones
func writeRelays(w io.Writer) {
	for _, e := range strings.Split(*relays, ",") {
		if id, hp, ok := strings.Cut(strings.TrimSpace(e), "="); ok && id != "" && hp != "" {
			io.WriteString(w, "RELAY "+strings.ToUpper(id)+" "+hp+"\n")
		}
	}
	communityMu.Lock()
	for _, c := range community {
		io.WriteString(w, "RELAY "+c+" COMMUNITY\n")
	}
	communityMu.Unlock()
	io.WriteString(w, "END\n")
}
