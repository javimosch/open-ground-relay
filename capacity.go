// (R7) how many battles this relay takes: -max-sessions if set, else worked out from the machine (open-file limit, free RAM) and
// -max-mbps (the uplink the owner gives it). PROBE reports "used/cap" so games pick a relay with room; a host past the cap is refused.
package main

import (
	"bufio"
	"flag"
	"log"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

var maxMbps = flag.Float64("max-mbps", 0, "uplink for the relay in Mbit/s (both ways); 0: not limited. Sets the cap with the machine's limits, and new hosts are refused above 90 %")

const (
	connsPerBattle = 6         // 4 players + the control link + a spare
	ramPerBattle   = 256 << 10 // buffers and goroutines, generous
	mbpsPerBattle  = 0.1       // a lockstep battle sends a few KB/s
	minCap         = 10
)

var sessionCap int
var rateBps int64 // bytes/s piped over the last 5 s

func memAvailable() int64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return -1
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if fs := strings.Fields(sc.Text()); len(fs) >= 2 && fs[0] == "MemAvailable:" {
			kb, _ := strconv.ParseInt(fs[1], 10, 64)
			return kb << 10
		}
	}
	return -1
}

// autoCap: the smallest of the machine's limits; why says which one
func autoCap() (int, string) {
	best, why := 1<<30, ""
	try := func(n int, w string) {
		if n < best {
			best, why = n, w
		}
	}
	var rl syscall.Rlimit
	if syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rl) == nil && rl.Cur > 0 {
		try(int((rl.Cur-32)/connsPerBattle), "open-file limit "+strconv.FormatUint(uint64(rl.Cur), 10))
	}
	if m := memAvailable(); m > 0 {
		try(int(m/2/ramPerBattle), "half the free RAM ("+strconv.FormatInt(m>>20, 10)+" MB)")
	}
	if *maxMbps > 0 {
		try(int(*maxMbps/mbpsPerBattle), "-max-mbps "+strconv.FormatFloat(*maxMbps, 'g', -1, 64))
	}
	if best == 1<<30 {
		return 200, "no limit found"
	}
	if best < minCap {
		best = minCap
	}
	return best, why
}

func setCap() {
	if *maxSessions > 0 {
		sessionCap = *maxSessions
		log.Printf("cap %d battles (-max-sessions)", sessionCap)
		return
	}
	var why string
	sessionCap, why = autoCap()
	log.Printf("cap %d battles (auto: %s)", sessionCap, why)
}

// the piped rate, sampled every 5 s
func watchRate() {
	last := atomic.LoadInt64(&rateLast)
	for range time.Tick(5 * time.Second) {
		now := atomic.LoadInt64(&pipedTotal)
		atomic.StoreInt64(&rateBps, (now-last)/5)
		last = now
	}
}

var pipedTotal, rateLast int64

// full: no room for one more host (count or bandwidth)
func full(sessions int) bool {
	if sessions >= sessionCap {
		return true
	}
	return *maxMbps > 0 && float64(atomic.LoadInt64(&rateBps))*8/1e6 > 0.9**maxMbps
}
