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
	registryEvery = flag.Duration("registry-every", time.Hour, "how often the registry is fetched and its relays probed")
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
	community   []string // "ID host:port", live at the last sweep
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
	return err == nil && strings.TrimSpace(l) == "PROBE "+e.ID
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
	var live []string
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, e := range es {
		wg.Add(1)
		go func(e regEntry) {
			defer wg.Done()
			if probe(e) {
				mu.Lock()
				live = append(live, e.ID+" "+net.JoinHostPort(e.Host, fmt.Sprint(e.Port)))
				mu.Unlock()
			} else {
				log.Printf("registry: %s (%s:%d) does not answer PROBE %s: not served", e.ID, e.Host, e.Port, e.ID)
			}
		}(e)
	}
	wg.Wait()
	communityMu.Lock()
	community = live
	communityMu.Unlock()
	log.Printf("registry: %d community relays live of %d listed", len(live), len(es))
}

func watchRegistry() {
	if *registryURL == "" {
		return
	}
	for {
		sweepRegistry()
		time.Sleep(*registryEvery)
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
