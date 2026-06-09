package main

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"
)

// snapshot is the cached result of the most recent probe cycle.
type snapshot struct {
	GeneratedAt time.Time      `json:"generated_at"`
	DurationMS  int64          `json:"duration_ms"`
	Results     []TargetResult `json:"results"`
}

// server holds the latest snapshot behind a mutex and re-probes on an interval.
type server struct {
	prober   *Prober
	targets  []Target
	interval time.Duration

	mu   sync.RWMutex
	snap snapshot
}

// serveHTTP runs an initial probe, then serves the cached results over HTTP and
// re-probes every interval. It blocks forever.
func serveHTTP(addr string, p *Prober, targets []Target, interval time.Duration) error {
	s := &server{prober: p, targets: targets, interval: interval}

	log.Printf("running initial probe of %d targets...", len(targets))
	s.probe()

	go s.loop()

	mux := http.NewServeMux()
	mux.HandleFunc("/results", s.handleResults)
	// NOTE: Google Front End reserves "/healthz" on *.run.app and returns its own
	// 404 before the request reaches the container, so expose the health check
	// under GFE-safe aliases too. Monitor "/status" from Uptime Kuma.
	mux.HandleFunc("/status", s.handleHealthz)
	mux.HandleFunc("/livez", s.handleHealthz)
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/", s.handleHuman)

	log.Printf("serving on %s (re-probing every %s)", addr, interval)
	log.Printf("  GET /          human-readable table")
	log.Printf("  GET /results   full JSON snapshot")
	log.Printf("  GET /status    200 if no hard-down, 503 otherwise (also /livez, /healthz)")
	return http.ListenAndServe(addr, mux)
}

func (s *server) loop() {
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for range t.C {
		s.probe()
	}
}

func (s *server) probe() {
	start := time.Now()
	results := s.prober.Run(s.targets)
	snap := snapshot{
		GeneratedAt: start,
		DurationMS:  time.Since(start).Milliseconds(),
		Results:     results,
	}
	s.mu.Lock()
	s.snap = snap
	s.mu.Unlock()

	down := hardDownTargets(results)
	if len(down) > 0 {
		log.Printf("probe complete in %dms: HARD-DOWN %v", snap.DurationMS, down)
	} else {
		log.Printf("probe complete in %dms: all clear", snap.DurationMS)
	}
}

func (s *server) current() snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snap
}

func (s *server) handleResults(w http.ResponseWriter, r *http.Request) {
	snap := s.current()
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(snap)
}

// handleHealthz returns 503 when any target is a hard down, so the prober is
// itself monitorable (e.g. by Uptime Kuma).
func (s *server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	snap := s.current()
	down := hardDownTargets(snap.Results)
	body := map[string]any{
		"status":       "ok",
		"generated_at": snap.GeneratedAt,
		"hard_down":    down,
	}
	code := http.StatusOK
	if len(down) > 0 {
		body["status"] = "degraded"
		code = http.StatusServiceUnavailable
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

func (s *server) handleHuman(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	snap := s.current()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	writeHuman(w, snap.Results)
}

func hardDownTargets(results []TargetResult) []string {
	var out []string
	for _, r := range results {
		if r.Status.hardDown() {
			out = append(out, r.Target)
		}
	}
	return out
}
