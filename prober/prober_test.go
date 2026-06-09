package main

import (
	"net"
	"testing"
	"time"
)

func testProber(stub handshakeSetFunc, enum func(string, time.Duration) ([]NS, error), q func(string, string, time.Duration) ([]net.IP, error)) *Prober {
	return &Prober{
		CountFloor:         5,
		RatioFloor:         0.5,
		DivergenceFactor:   0.5,
		MedianHealthyFloor: 0.5,
		Concurrency:        4,
		DNSTimeout:         time.Second,
		enumerateNS:        enum,
		queryA:             q,
		handshakeSet:       stub,
	}
}

// TestDivergenceFlagsOutlierNS reproduces the incident: five healthy
// nameservers and one (ns1) serving mostly-dead peers. Divergence must flag ns1
// and only ns1.
func TestDivergenceFlagsOutlierNS(t *testing.T) {
	nsList := []NS{
		{Name: "ns1", IP: "10.0.0.1"}, {Name: "ns2", IP: "10.0.0.2"},
		{Name: "ns3", IP: "10.0.0.3"}, {Name: "ns4", IP: "10.0.0.4"},
	}
	// ns1 returns 10 IPs of which only 1 is in the live set; ns2..4 return
	// healthy sets fully live.
	healthy := ips("9.1.1.1", "9.1.1.2", "9.1.1.3", "9.1.1.4", "9.1.1.5", "9.1.1.6")
	stale := ips("8.1.1.1", "8.1.1.2", "8.1.1.3", "8.1.1.4", "8.1.1.5",
		"8.1.1.6", "8.1.1.7", "8.1.1.8", "8.1.1.9", "9.1.1.1")
	p := testProber(
		func(_ string, in []net.IP, _ int) (map[string]time.Duration, error) {
			live := map[string]time.Duration{}
			for _, ip := range in {
				if ip.String()[:2] == "9." { // healthy peers are live
					live[ip.String()] = time.Millisecond
				}
			}
			return live, nil
		},
		func(string, time.Duration) ([]NS, error) { return nsList, nil },
		func(nsIP, host string, _ time.Duration) ([]net.IP, error) {
			if nsIP == "10.0.0.1" {
				return stale, nil
			}
			return healthy, nil
		},
	)
	res := p.ProbeTarget(Target{Name: "T", Hostname: "seed.example", Network: "mainnet"})
	if res.Status != StatusDiverging {
		t.Fatalf("target status = %s, want %s", res.Status, StatusDiverging)
	}
	for _, ns := range res.Nameservers {
		want := StatusUp
		if ns.NS == "ns1" {
			want = StatusDiverging
		}
		if ns.Status != want {
			t.Fatalf("%s: status %s, want %s", ns.NS, ns.Status, want)
		}
	}
}

// TestUniformLowIsInconclusive is the cooldown-artifact guard: when ALL
// nameservers are uniformly low (no outlier), nothing is flagged as a hard
// failure — it is INCONCLUSIVE, not DOWN.
func TestUniformLowIsInconclusive(t *testing.T) {
	nsList := []NS{{Name: "ns1", IP: "10.0.0.1"}, {Name: "ns2", IP: "10.0.0.2"}}
	set := ips("1.1.1.1", "2.2.2.2", "3.3.3.3", "4.4.4.4", "5.5.5.5", "6.6.6.6", "7.7.7.7", "8.8.8.8")
	p := testProber(
		func(_ string, in []net.IP, _ int) (map[string]time.Duration, error) {
			// Only 2 of 8 live for everyone -> uniformly low, no divergence.
			return map[string]time.Duration{"1.1.1.1": 1, "2.2.2.2": 1}, nil
		},
		func(string, time.Duration) ([]NS, error) { return nsList, nil },
		func(nsIP, host string, _ time.Duration) ([]net.IP, error) { return set, nil },
	)
	res := p.ProbeTarget(Target{Name: "T", Hostname: "seed.example", Network: "mainnet"})
	if res.Status != StatusInconclusive {
		t.Fatalf("uniform-low target status = %s, want %s", res.Status, StatusInconclusive)
	}
	if res.Status.hardDown() {
		t.Fatalf("INCONCLUSIVE must not be a hard down")
	}
}

// TestHealthyAllUp: everyone fully live -> UP.
func TestHealthyAllUp(t *testing.T) {
	nsList := []NS{{Name: "ns1", IP: "10.0.0.1"}, {Name: "ns2", IP: "10.0.0.2"}}
	set := ips("1.1.1.1", "2.2.2.2", "3.3.3.3", "4.4.4.4", "5.5.5.5", "6.6.6.6")
	p := testProber(
		func(_ string, in []net.IP, _ int) (map[string]time.Duration, error) {
			live := map[string]time.Duration{}
			for _, ip := range in {
				live[ip.String()] = time.Millisecond
			}
			return live, nil
		},
		func(string, time.Duration) ([]NS, error) { return nsList, nil },
		func(nsIP, host string, _ time.Duration) ([]net.IP, error) { return set, nil },
	)
	res := p.ProbeTarget(Target{Name: "T", Hostname: "seed.example", Network: "mainnet"})
	if res.Status != StatusUp {
		t.Fatalf("healthy target status = %s, want %s", res.Status, StatusUp)
	}
}

func TestProbeTargetDNSFailure(t *testing.T) {
	nsList := []NS{{Name: "ns1", IP: "10.0.0.1"}}
	p := testProber(
		func(_ string, in []net.IP, _ int) (map[string]time.Duration, error) {
			return map[string]time.Duration{}, nil
		},
		func(string, time.Duration) ([]NS, error) { return nsList, nil },
		func(nsIP, host string, _ time.Duration) ([]net.IP, error) { return nil, nil },
	)
	res := p.ProbeTarget(Target{Name: "T", Hostname: "seed.example", Network: "mainnet"})
	if res.Status != StatusDownDNS {
		t.Fatalf("empty-records target status = %s, want %s", res.Status, StatusDownDNS)
	}
}

// TestUnionHandshakedOnce locks in the order-bias fix: overlapping nameservers
// are scored by handshaking the deduped union exactly once.
func TestUnionHandshakedOnce(t *testing.T) {
	calls := 0
	nsList := []NS{{Name: "ns1", IP: "10.0.0.1"}, {Name: "ns2", IP: "10.0.0.2"}}
	p := testProber(
		func(_ string, in []net.IP, _ int) (map[string]time.Duration, error) {
			calls++
			if len(in) != 5 { // 1.1.1.1..4.4.4.4 + 9.9.9.9
				t.Fatalf("handshakeSet got %d IPs, want 5 (deduped union)", len(in))
			}
			return map[string]time.Duration{"1.1.1.1": 1, "2.2.2.2": 1, "3.3.3.3": 1}, nil
		},
		func(string, time.Duration) ([]NS, error) { return nsList, nil },
		func(nsIP, host string, _ time.Duration) ([]net.IP, error) {
			if nsIP == "10.0.0.1" {
				return ips("1.1.1.1", "2.2.2.2", "3.3.3.3", "4.4.4.4"), nil
			}
			return ips("1.1.1.1", "2.2.2.2", "3.3.3.3", "9.9.9.9"), nil
		},
	)
	p.CountFloor = 1 // tiny synthetic counts; only testing union + attribution
	res := p.ProbeTarget(Target{Name: "T", Hostname: "seed.example", Network: "mainnet"})
	if calls != 1 {
		t.Fatalf("handshakeSet called %d times, want exactly 1", calls)
	}
	// Both share the 3 live peers -> 3/4 = 0.75, no divergence -> UP.
	for _, ns := range res.Nameservers {
		if ns.Live != 3 || ns.Status != StatusUp {
			t.Fatalf("%s: live=%d status=%s, want 3 / UP", ns.NS, ns.Live, ns.Status)
		}
	}
}

func TestMedian(t *testing.T) {
	if got := median([]float64{0.2, 0.8, 0.5}); got != 0.5 {
		t.Fatalf("median odd = %v, want 0.5", got)
	}
	if got := median([]float64{0.2, 0.4, 0.6, 0.8}); got != 0.5 {
		t.Fatalf("median even = %v, want 0.5", got)
	}
}

func TestDedupeIPs(t *testing.T) {
	out := dedupeIPs(ips("1.1.1.1", "2.2.2.2", "1.1.1.1"))
	if len(out) != 2 {
		t.Fatalf("dedupeIPs len = %d, want 2", len(out))
	}
}

func ips(ss ...string) []net.IP {
	var out []net.IP
	for _, s := range ss {
		out = append(out, net.ParseIP(s))
	}
	return out
}
