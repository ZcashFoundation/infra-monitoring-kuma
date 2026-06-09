// Command seeder-prober validates Zcash DNS seeders per-nameserver.
//
// For each configured seeder it enumerates the zone's authoritative nameservers
// (ns1..nsN), queries each one DIRECTLY for the seeder's A records, and verifies
// those IPs are live healthy nodes by performing the dnsseeder's own Zcash
// version/verack handshake. It reports which nameserver, if any, is serving dead
// peers -- the signal a recursive DNS check structurally cannot see.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type config struct {
	Targets []Target `yaml:"targets"`
}

func main() {
	var (
		configPath  = flag.String("config", "seeders.yaml", "path to seeders config YAML")
		jsonOut     = flag.Bool("json", false, "emit JSON instead of a human table (one-shot mode)")
		serve       = flag.String("serve", "", "if set (e.g. :8080), run as an HTTP server instead of one-shot")
		interval    = flag.Duration("interval", 15*time.Minute, "re-probe interval in --serve mode (keep >= ~15m; nodes rate-limit rapid reprobes)")
		divFactor   = flag.Float64("divergence-factor", 0.5, "a nameserver is DIVERGING if its live ratio < factor * sibling median")
		medFloor    = flag.Float64("median-healthy-floor", 0.5, "only judge divergence when the sibling median ratio is at least this")
		countFloor  = flag.Int("count-floor", 5, "absolute (soft): min live peers below which a non-diverging NS is INCONCLUSIVE")
		ratioFloor  = flag.Float64("ratio-floor", 0.5, "absolute (soft): min live ratio below which a non-diverging NS is INCONCLUSIVE")
		concurrency = flag.Int("concurrency", 16, "max concurrent handshakes")
		dnsTimeout  = flag.Duration("dns-timeout", 5*time.Second, "timeout per DNS query")
	)
	flag.Parse()

	cfg, err := loadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}

	p := NewProber(*countFloor, *ratioFloor, *divFactor, *medFloor, *concurrency, *dnsTimeout)

	if *serve != "" {
		if err := serveHTTP(*serve, p, cfg.Targets, *interval); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(2)
		}
		return
	}

	results := p.Run(cfg.Targets)
	anyDown := false
	for _, r := range results {
		if r.Status.hardDown() {
			anyDown = true
		}
	}

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(results); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(2)
		}
	} else {
		writeHuman(os.Stdout, results)
	}

	if anyDown {
		os.Exit(1)
	}
}

func loadConfig(path string) (*config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config %s: %w", path, err)
	}
	var cfg config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config %s: %w", path, err)
	}
	if len(cfg.Targets) == 0 {
		return nil, fmt.Errorf("config %s has no targets", path)
	}
	for i, t := range cfg.Targets {
		if t.Hostname == "" || t.Network == "" {
			return nil, fmt.Errorf("target #%d (%q) needs hostname and network", i+1, t.Name)
		}
	}
	return &cfg, nil
}

func writeHuman(w io.Writer, results []TargetResult) {
	for _, r := range results {
		fmt.Fprintf(w, "\n%s  [%s]  %s\n", r.Target, r.Network, r.Status)
		fmt.Fprintf(w, "  %s\n", r.Hostname)
		if len(r.Nameservers) == 0 {
			fmt.Fprintf(w, "    %s\n", r.Message)
			continue
		}
		for _, ns := range r.Nameservers {
			line := fmt.Sprintf("    %-22s %-16s live %2d/%-2d", ns.NS, ns.Status, ns.Live, ns.Records)
			if ns.Records > 0 {
				line += fmt.Sprintf("  ratio %.2f", ns.Ratio)
			}
			if ns.AvgPingMS > 0 {
				line += fmt.Sprintf("  ~%dms", ns.AvgPingMS)
			}
			if ns.Detail != "" {
				line += "  " + ns.Detail
			}
			fmt.Fprintln(w, line)
		}
	}
	fmt.Fprintln(w)
}
