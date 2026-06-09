package main

import (
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zcashfoundation/dnsseeder/zcash"
	"github.com/zcashfoundation/dnsseeder/zcash/network"
)

// Status is the tiered health verdict for a nameserver or a target.
//
// The primary signal is DIVERGENCE: a nameserver whose live-peer ratio is far
// below its siblings'. That is robust to the node-side rate-limiting that
// depresses every nameserver uniformly when we probe too often, and it matches
// the real incident (one stale nameserver among healthy ones). Absolute low
// liveness with no divergence is reported as INCONCLUSIVE, not a hard failure,
// because a single run can land inside a self-induced cooldown window.
type Status string

const (
	StatusUp Status = "UP"
	// StatusInconclusive: records returned, absolute liveness is low, but the
	// nameserver is NOT an outlier vs its siblings. Likely probe cadence /
	// cooldown rather than a real fault. Soft signal.
	StatusInconclusive Status = "INCONCLUSIVE"
	// StatusDiverging: this nameserver's live ratio is well below its siblings'
	// median while the cohort is otherwise healthy. The real fault signal.
	StatusDiverging Status = "DOWN(divergence)"
	// StatusDownDNS: the nameserver did not answer or returned no records.
	StatusDownDNS Status = "DOWN(dns)"
)

func (s Status) severity() int {
	switch s {
	case StatusUp:
		return 0
	case StatusInconclusive:
		return 1
	case StatusDiverging:
		return 2
	case StatusDownDNS:
		return 3
	default:
		return 4
	}
}

// hardDown reports whether a status is a real failure (drives exit code), as
// opposed to the soft/inconclusive case.
func (s Status) hardDown() bool {
	return s == StatusDiverging || s == StatusDownDNS
}

// Target is one seeder under test.
type Target struct {
	Name        string   `yaml:"name"`
	Hostname    string   `yaml:"hostname"`
	Network     string   `yaml:"network"` // "mainnet" | "testnet"
	Nameservers []string `yaml:"nameservers,omitempty"`
}

// NSResult is the per-nameserver outcome.
type NSResult struct {
	NS        string  `json:"ns"`
	NSIP      string  `json:"ns_ip"`
	Records   int     `json:"records"`
	Live      int     `json:"live"`
	Ratio     float64 `json:"ratio"`
	AvgPingMS int64   `json:"avg_ping_ms,omitempty"`
	Status    Status  `json:"status"`
	Detail    string  `json:"detail,omitempty"`
}

// TargetResult is the rolled-up outcome for a seeder across all its nameservers.
type TargetResult struct {
	Target      string     `json:"target"`
	Hostname    string     `json:"hostname"`
	Network     string     `json:"network"`
	Status      Status     `json:"status"`
	Message     string     `json:"message"`
	Nameservers []NSResult `json:"nameservers"`
}

// handshakeSetFunc handshakes a set of unique IPs once each and returns the
// latency of those that are live (key present == live). Probing each unique peer
// exactly once is essential: the nameservers serve heavily overlapping peer
// sets, and Zcash nodes refuse rapid repeat connections from the same IP.
type handshakeSetFunc func(net string, ips []net.IP, concurrency int) (live map[string]time.Duration, err error)

// Prober holds thresholds and injectable I/O for testability.
type Prober struct {
	CountFloor         int           // absolute: min live peers (secondary/soft)
	RatioFloor         float64       // absolute: min live ratio (secondary/soft)
	DivergenceFactor   float64       // primary: NS is diverging if ratio < factor*median
	MedianHealthyFloor float64       // only judge divergence when sibling median >= this
	Concurrency        int           // max concurrent handshakes
	DNSTimeout         time.Duration // timeout per DNS query

	enumerateNS  func(host string, timeout time.Duration) ([]NS, error)
	queryA       func(nsIP, host string, timeout time.Duration) ([]net.IP, error)
	handshakeSet handshakeSetFunc
}

// NewProber returns a Prober wired to the real DNS + dnsseeder handshake.
func NewProber(countFloor int, ratioFloor, divergenceFactor, medianHealthyFloor float64, concurrency int, dnsTimeout time.Duration) *Prober {
	return &Prober{
		CountFloor:         countFloor,
		RatioFloor:         ratioFloor,
		DivergenceFactor:   divergenceFactor,
		MedianHealthyFloor: medianHealthyFloor,
		Concurrency:        concurrency,
		DNSTimeout:         dnsTimeout,
		enumerateNS:        enumerateNS,
		queryA:             queryAuthoritativeA,
		handshakeSet:       realHandshakeSet,
	}
}

// targetState holds the per-target DNS results gathered before handshaking.
type targetState struct {
	t            Target
	nsList       []NS
	nsIPs        [][]net.IP
	nsErr        []error
	discoveryErr error
}

// Run probes all targets. It gathers every nameserver's A records first, then
// handshakes one global union PER NETWORK exactly once, then attributes liveness
// and classifies. The per-network union avoids re-contacting (and thus
// rate-limiting) peers shared across seeders within a single run.
func (p *Prober) Run(targets []Target) []TargetResult {
	// Phase 1: resolve nameservers and query each for its A records.
	states := make([]*targetState, len(targets))
	unionByNet := map[string]map[string]net.IP{}
	for i, t := range targets {
		st := &targetState{t: t}
		states[i] = st

		var nsList []NS
		var err error
		if len(t.Nameservers) > 0 {
			nsList, err = resolveOverrides(t.Nameservers, p.DNSTimeout)
		} else {
			nsList, err = p.enumerateNS(t.Hostname, p.DNSTimeout)
		}
		if err != nil || len(nsList) == 0 {
			if err != nil {
				st.discoveryErr = err
			} else {
				st.discoveryErr = fmt.Errorf("no nameservers found")
			}
			continue
		}
		st.nsList = nsList
		st.nsIPs = make([][]net.IP, len(nsList))
		st.nsErr = make([]error, len(nsList))
		for j, ns := range nsList {
			ips, qErr := p.queryA(ns.IP, t.Hostname, p.DNSTimeout)
			st.nsErr[j] = qErr
			ips = dedupeIPs(ips)
			st.nsIPs[j] = ips
			if _, ok := unionByNet[t.Network]; !ok {
				unionByNet[t.Network] = map[string]net.IP{}
			}
			for _, ip := range ips {
				unionByNet[t.Network][ip.String()] = ip
			}
		}
	}

	// Phase 2: handshake each network's union exactly once.
	liveByNet := map[string]map[string]time.Duration{}
	for nw, set := range unionByNet {
		list := make([]net.IP, 0, len(set))
		for _, ip := range set {
			list = append(list, ip)
		}
		if l, err := p.handshakeSet(nw, list, p.Concurrency); err == nil {
			liveByNet[nw] = l
		} else {
			liveByNet[nw] = map[string]time.Duration{}
		}
	}

	// Phase 3: attribute liveness, then classify per target.
	results := make([]TargetResult, 0, len(targets))
	for _, st := range states {
		res := TargetResult{Target: st.t.Name, Hostname: st.t.Hostname, Network: st.t.Network}
		if st.discoveryErr != nil {
			res.Status = StatusDownDNS
			res.Message = "nameserver discovery failed: " + st.discoveryErr.Error()
			results = append(results, res)
			continue
		}
		live := liveByNet[st.t.Network]
		for j, ns := range st.nsList {
			res.Nameservers = append(res.Nameservers, measureNS(ns, st.nsIPs[j], st.nsErr[j], live))
		}
		res.Status, res.Message = p.classify(res.Nameservers)
		results = append(results, res)
	}
	return results
}

// ProbeTarget probes a single target.
func (p *Prober) ProbeTarget(t Target) TargetResult {
	return p.Run([]Target{t})[0]
}

// measureNS fills records/live/ratio/ping for a nameserver. It only sets a
// status for the DNS-failure cases; liveness classification happens in classify
// once all sibling ratios are known.
func measureNS(ns NS, ips []net.IP, qErr error, live map[string]time.Duration) NSResult {
	r := NSResult{NS: ns.Name, NSIP: ns.IP}
	if qErr != nil {
		r.Status = StatusDownDNS
		r.Detail = "query failed: " + qErr.Error()
		return r
	}
	r.Records = len(ips)
	if r.Records == 0 {
		r.Status = StatusDownDNS
		r.Detail = "no A records"
		return r
	}
	var total time.Duration
	for _, ip := range ips {
		if d, ok := live[ip.String()]; ok {
			r.Live++
			total += d
		}
	}
	r.Ratio = float64(r.Live) / float64(r.Records)
	if r.Live > 0 {
		r.AvgPingMS = (total / time.Duration(r.Live)).Milliseconds()
	}
	return r
}

// classify applies the divergence-primary rule across a target's nameservers and
// rolls up to a target status + message.
func (p *Prober) classify(results []NSResult) (Status, string) {
	// Sibling cohort = nameservers that returned records.
	var ratios []float64
	var withRecords []int
	for i := range results {
		if results[i].Status == StatusDownDNS {
			continue
		}
		ratios = append(ratios, results[i].Ratio)
		withRecords = append(withRecords, i)
	}
	med := median(ratios)

	for _, i := range withRecords {
		r := &results[i]
		switch {
		case len(withRecords) >= 2 && med >= p.MedianHealthyFloor && r.Ratio < p.DivergenceFactor*med:
			r.Status = StatusDiverging
			r.Detail = fmt.Sprintf("outlier: ratio %.2f vs sibling median %.2f", r.Ratio, med)
		case r.Live < p.CountFloor || r.Ratio < p.RatioFloor:
			r.Status = StatusInconclusive
			r.Detail = "low liveness, no sibling divergence (may be probe cadence/cooldown)"
		default:
			r.Status = StatusUp
		}
	}

	return rollup(results)
}

// rollup sets a target's status to its worst nameserver and builds a one-line
// message naming the offenders.
func rollup(results []NSResult) (Status, string) {
	if len(results) == 0 {
		return StatusDownDNS, "no nameservers probed"
	}
	worst := StatusUp
	var parts []string
	for _, r := range results {
		if r.Status.severity() > worst.severity() {
			worst = r.Status
		}
		switch r.Status {
		case StatusUp:
			parts = append(parts, fmt.Sprintf("%s=UP(%d/%d)", r.NS, r.Live, r.Records))
		case StatusInconclusive:
			parts = append(parts, fmt.Sprintf("%s=INCONCLUSIVE(%d/%d)", r.NS, r.Live, r.Records))
		case StatusDiverging:
			parts = append(parts, fmt.Sprintf("%s=DIVERGING(%d/%d)", r.NS, r.Live, r.Records))
		case StatusDownDNS:
			parts = append(parts, fmt.Sprintf("%s=DOWN-dns", r.NS))
		}
	}
	return worst, strings.Join(parts, " ")
}

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// realHandshakeSet validates each unique IP with the dnsseeder's own Zcash
// version/verack handshake on the network's default p2p port, returning the
// latency of those that are live. Connections are torn down before returning to
// keep file descriptors bounded.
func realHandshakeSet(net_ string, ips []net.IP, concurrency int) (map[string]time.Duration, error) {
	znet, err := toZcashNetwork(net_)
	if err != nil {
		return nil, err
	}
	seeder, err := zcash.NewSeeder(znet)
	if err != nil {
		return nil, err
	}
	defer seeder.DisconnectAllPeers()

	uniq := dedupeIPs(ips)
	if concurrency < 1 {
		concurrency = 1
	}
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex
	live := make(map[string]time.Duration, len(uniq))

	for _, ip := range uniq {
		wg.Add(1)
		sem <- struct{}{}
		go func(addr string) {
			defer wg.Done()
			defer func() { <-sem }()
			start := time.Now()
			// ConnectOnDefaultPort runs the full TCP dial + version/verack with
			// dnsseeder's own 5s dial / 5s handshake timeouts and per-network
			// minimum protocol version. nil error == live healthy node.
			err := seeder.ConnectOnDefaultPort(addr)
			d := time.Since(start)
			if err == nil {
				mu.Lock()
				live[addr] = d
				mu.Unlock()
			}
		}(ip.String())
	}
	wg.Wait()
	return live, nil
}

func toZcashNetwork(n string) (network.Network, error) {
	switch strings.ToLower(n) {
	case "mainnet":
		return network.Mainnet, nil
	case "testnet":
		return network.Testnet, nil
	case "regtest":
		return network.Regtest, nil
	default:
		return 0, fmt.Errorf("unknown network %q (want mainnet|testnet)", n)
	}
}

func dedupeIPs(ips []net.IP) []net.IP {
	seen := make(map[string]struct{}, len(ips))
	var out []net.IP
	for _, ip := range ips {
		k := ip.String()
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, ip)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}
