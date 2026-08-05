package main

import (
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/miekg/dns"
)

// NS is one authoritative nameserver for a seeder zone: its hostname and the IP
// we will query directly.
type NS struct {
	Name string `json:"name"`
	IP   string `json:"ip"`
}

// systemResolvers returns recursive resolvers to try in order: every server in
// /etc/resolv.conf, then public fallbacks. We try several because the first
// configured resolver may be a VPN/local one that doesn't answer from here.
func systemResolvers() []string {
	var out []string
	if conf, err := dns.ClientConfigFromFile("/etc/resolv.conf"); err == nil {
		port := conf.Port
		if port == "" {
			port = "53"
		}
		for _, s := range conf.Servers {
			out = append(out, net.JoinHostPort(s, port))
		}
	}
	out = append(out, "1.1.1.1:53", "8.8.8.8:53")
	return out
}

// rootServers bootstrap iterative resolution. The seeder zones do not serve NS
// records at their own apex — the delegation lives only in the parent zone — so
// a recursive NS lookup returns nothing. We must walk down from the root and
// capture the delegation NS set from the referral chain.
var rootServers = []string{
	"198.41.0.4:53",     // a.root-servers.net
	"199.9.14.201:53",   // b.root-servers.net
	"192.33.4.12:53",    // c.root-servers.net
	"199.7.91.13:53",    // d.root-servers.net
	"192.203.230.10:53", // e.root-servers.net
}

// enumerateNS finds the authoritative nameservers serving host by iterative
// resolution: it queries the root, follows each referral one zone cut deeper,
// and returns the NS set of the deepest delegation (e.g. ns1..ns6.zfnd.org for
// mainnet.seeder.zfnd.org).
func enumerateNS(host string, timeout time.Duration) ([]NS, error) {
	fqdn := dns.Fqdn(host)
	servers := rootServers
	var lastNS []NS
	lastZone := "."

	for depth := 0; depth < 16; depth++ {
		resp, err := queryReferral(servers, fqdn, timeout)
		if err != nil {
			return nil, err
		}
		nsRecs := nsRecords(resp)
		if len(nsRecs) == 0 {
			break // reached the authoritative zone; no deeper delegation
		}
		zone := nsRecs[0].Header().Name
		if !strictlyDeeper(zone, lastZone) {
			break // no progress -> we are at the serving zone
		}
		entries := resolveNSEntries(nsRecs, resp, timeout)
		if len(entries) == 0 {
			break
		}
		lastNS = entries
		lastZone = zone
		servers = nsServerAddrs(entries)

		if resp.Authoritative && len(resp.Answer) > 0 {
			break
		}
	}

	if len(lastNS) == 0 {
		return nil, fmt.Errorf("no authoritative nameservers found for %s", host)
	}
	return lastNS, nil
}

// queryReferral asks each server in turn (recursion disabled) for host's A
// record and returns the first response, which is either a referral (NS in the
// authority section) or the authoritative answer.
func queryReferral(servers []string, fqdn string, timeout time.Duration) (*dns.Msg, error) {
	c := &dns.Client{Timeout: timeout}
	m := new(dns.Msg)
	m.SetQuestion(fqdn, dns.TypeA)
	m.RecursionDesired = false
	var lastErr error
	for _, s := range servers {
		r, _, err := c.Exchange(m, s)
		if err != nil {
			lastErr = err
			continue
		}
		if r.Truncated {
			c.Net = "tcp"
			r, _, err = c.Exchange(m, s)
			c.Net = ""
			if err != nil {
				lastErr = err
				continue
			}
		}
		return r, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no servers answered")
	}
	return nil, lastErr
}

// nsRecords returns NS records from the authority section, falling back to the
// answer section (some servers answer NS questions directly).
func nsRecords(r *dns.Msg) []*dns.NS {
	var out []*dns.NS
	for _, rr := range r.Ns {
		if ns, ok := rr.(*dns.NS); ok {
			out = append(out, ns)
		}
	}
	if len(out) == 0 {
		for _, rr := range r.Answer {
			if ns, ok := rr.(*dns.NS); ok {
				out = append(out, ns)
			}
		}
	}
	return out
}

// resolveNSEntries pairs each NS name with an IP, preferring glue A records from
// the response's additional section and falling back to the system resolver.
func resolveNSEntries(nsRecs []*dns.NS, resp *dns.Msg, timeout time.Duration) []NS {
	glue := map[string]string{}
	for _, rr := range resp.Extra {
		if a, ok := rr.(*dns.A); ok {
			glue[a.Header().Name] = a.A.String()
		}
	}
	var out []NS
	for _, ns := range nsRecs {
		name := ns.Ns
		ip := glue[name]
		if ip == "" {
			if ips, err := lookupA(name, timeout); err == nil && len(ips) > 0 {
				ip = ips[0].String()
			}
		}
		if ip != "" {
			out = append(out, NS{Name: strings.TrimSuffix(name, "."), IP: ip})
		}
	}
	return out
}

func nsServerAddrs(entries []NS) []string {
	var out []string
	for _, e := range entries {
		out = append(out, net.JoinHostPort(e.IP, "53"))
	}
	return out
}

// strictlyDeeper reports whether zone a is below zone b in the DNS hierarchy
// (more labels), so we only follow referrals that make downward progress.
func strictlyDeeper(a, b string) bool {
	return dns.CountLabel(a) > dns.CountLabel(b)
}

// lookupA does a recursive A lookup for name, trying each system resolver until
// one returns an answer.
func lookupA(name string, timeout time.Duration) ([]net.IP, error) {
	c := &dns.Client{Timeout: timeout}
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(name), dns.TypeA)
	m.RecursionDesired = true
	var lastErr error
	for _, resolver := range systemResolvers() {
		r, _, err := c.Exchange(m, resolver)
		if err != nil {
			lastErr = err
			continue
		}
		if ips := extractA(r); len(ips) > 0 {
			return ips, nil
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no A records for %s", name)
	}
	return nil, lastErr
}

// queryAuthoritativeA asks a specific nameserver (by IP) for the A records of
// host with recursion disabled, so we get that backend's own answer rather than
// a recursive resolver's cached/arbitrary pick. This is the heart of per-NS
// probing. Falls back to TCP if the UDP response is truncated.
func queryAuthoritativeA(nsIP, host string, timeout time.Duration) ([]net.IP, error) {
	c := &dns.Client{Timeout: timeout}
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(host), dns.TypeA)
	m.RecursionDesired = false
	addr := net.JoinHostPort(nsIP, "53")

	r, _, err := c.Exchange(m, addr)
	if err != nil {
		return nil, err
	}
	if r.Truncated {
		c.Net = "tcp"
		r, _, err = c.Exchange(m, addr)
		if err != nil {
			return nil, err
		}
	}
	if r.Rcode != dns.RcodeSuccess {
		return nil, fmt.Errorf("rcode %s", dns.RcodeToString[r.Rcode])
	}
	return extractA(r), nil
}

func extractA(r *dns.Msg) []net.IP {
	var ips []net.IP
	for _, a := range r.Answer {
		if rec, ok := a.(*dns.A); ok {
			ips = append(ips, rec.A)
		}
	}
	return ips
}

// resolveOverrides turns a list of nameserver hostnames-or-IPs (from a target's
// optional `nameservers:` config) into NS entries with resolved IPs.
func resolveOverrides(entries []string, timeout time.Duration) ([]NS, error) {
	var out []NS
	for _, e := range entries {
		if ip := net.ParseIP(e); ip != nil {
			out = append(out, NS{Name: e, IP: ip.String()})
			continue
		}
		ips, err := lookupA(e, timeout)
		if err != nil || len(ips) == 0 {
			return nil, fmt.Errorf("could not resolve nameserver %q", e)
		}
		out = append(out, NS{Name: e, IP: ips[0].String()})
	}
	return out, nil
}
