# Zcash DNS-seeder resilience: root cause & hardening notes

Written after the 2026-05-28 near-stall, to record *why* it happened and what
would prevent a recurrence. The monitoring prober in [`../prober`](../prober)
is the **detection** layer that came out of this; the items below are
**prevention/remediation** that live in other repos (dnsseeder, coredns-zcash,
Zebra, Cloudflare) and are for their owners to weigh — nothing here has been
changed in those systems.

Code references are to local clones at the time of writing:
`zcashfoundation/dnsseeder@v0.4.0`, `zcashfoundation/coredns-zcash`,
`ZcashFoundation/zebra`.

## What happened

The seeder zone `seeder.zfnd.org` is delegated to six authoritative
nameservers, `ns1..ns6.zfnd.org`, each a separate seeder instance. The old
`ns1` instance had an outdated CoreDNS `Corefile` (stale `bootstrap_peers`,
missing ZF's own nodes), so it bootstrapped a divergent peer set and began
serving stale/dead IPs. Resolvers hit `ns1` by default, so a large share of
nodes kept receiving dead peers and struggled to connect — nearly stalling the
network. Nothing detected or corrected it automatically.

## Why nothing self-corrected

### The seeder partly self-heals, but has gaps — and can't disable itself

- It **does** drop peers that fail re-test: `RefreshAddresses()` blacklists a
  peer whose handshake fails (`dnsseeder/zcash/client.go:449-464`).
- **No stale age-out.** A once-good peer lingers indefinitely unless a refresh
  actively re-tests and fails it; the `lastUpdate` timestamp is only used for
  blacklist forgetting (`zcash/address_book.go`).
- **No timeout on the refresh/crawl cycle** (`dnsseed/setup.go:193`). If the
  instance is isolated or its bootstrap peers are dead, the cycle can stall and
  the last-known (possibly all-dead) set keeps being served.
- **Readiness is one-way.** Once it has 10 good addresses, `Ready()` is true
  forever and never re-checked (`zcash/client.go:555`); it keeps answering even
  if the good set decays to zero-live.
- **No self-disable / SERVFAIL.** An unhealthy instance has no way to remove
  itself from rotation. The `ns1..ns6` delegation is static config at
  Cloudflare; a sick seeder can't evict its own NS record, and DNS has no
  health-based nameserver selection.
- **The container healthcheck is quality-blind:** `HEALTHCHECK … dig $DOMAIN`
  (`coredns-zcash/docker/Dockerfile:57`) only checks that DNS answers, not that
  the answers are good — the same blind spot the old Kuma monitor had.

### Zebra is resilient per-peer but blind per-nameserver

- It resolves seeds via the OS recursive resolver (`tokio::net::lookup_host`,
  `zebra-network/src/config.rs:362`) — no awareness of which nameserver
  answered; it takes whatever is cached.
- It unions all seed hostnames and accepts the first non-empty result, with no
  per-seeder retry (`config.rs:281-318`).
- Per peer it recovers (drops dead IPs, tries others) but slowly: 3s handshake
  timeout, ~119s before retrying a failed peer. A fresh/restarting node with no
  disk cache that gets a mostly-dead set can stall for minutes-to-hours; warm
  nodes ride it out — matching "existing nodes were fine once fixed."
- No logic notices "this seeder/nameserver keeps giving bad peers, prefer
  another."

### Net

DNS won't route around a bad nameserver, the seeder can't see or disable
itself, and Zebra can't compensate because it's per-nameserver blind. The only
component with a per-nameserver, quality-aware view is an external probe — hence
the prober.

## A second, non-obvious finding: active probing self-poisons

While building the prober we found that a full-handshake probe **rate-limits
itself**. The six nameservers serve heavily overlapping peer sets, and Zcash
nodes refuse rapid repeat connections from the same source IP. Measured
directly (ZFND mainnet, full version/verack handshake):

| run | condition | ns1 | ns2 | ns3 | ns4 | ns5 | ns6 |
|-----|-----------|-----|-----|-----|-----|-----|-----|
| baseline | after ~150s idle | 24/25 | 19/25 | 21/25 | 14/14 | 20/21 | 23/25 |
| immediate re-probe | seconds later | 4/25 | 8/25 | 8/25 | 4/14 | 4/21 | 5/25 |

The re-probe collapse is **uniform** across all six — they're all healthy; the
drop is our own cooldown. Two consequences shaped the prober and should shape
any future automation:

1. **Probe cadence must be ≥ the cooldown** (we use 15 min, matching the seeder
   crawl). Probing every couple of minutes would chronically false-alarm.
2. **Relative divergence, not absolute liveness, is the trustworthy signal.**
   Cooldown depresses every nameserver equally, so an outlier-vs-siblings test
   survives it; absolute thresholds do not. (The real incident — one stale
   nameserver among healthy ones — shows up precisely as divergence.)

This also argues against the network adopting *aggressive* active health-checks
of seeders: the seeder's own infrequent crawl is the right model.

## Recommended hardening (for the owners to consider)

Detection (the prober) tells you a backend has gone bad; it doesn't fix the
gaps above. Durable prevention lives upstream:

**dnsseeder / coredns-zcash**
- Add an overall timeout to the refresh/crawl cycle and a metric/log when a
  crawl fails to complete, so a stalled instance is visible.
- Make readiness re-evaluable: if the live set decays below the readiness floor,
  stop answering (SERVFAIL) so resolvers fail over to a healthy nameserver.
- Add stale age-out: drop peers not successfully re-validated within N crawl
  cycles.
- Make the container healthcheck **quality-aware**: fail if the instance hasn't
  validated ≥N peers within T, so the orchestrator restarts/removes it.

**Operational**
- Don't leave zombie/abandoned seeder instances in the delegation. Decommission
  or keep their `Corefile` `bootstrap_peers` current (the `ns1` trigger here).

**Zebra (optional, larger)**
- Consider lightweight per-seeder health/preference, or re-querying DNS when a
  large fraction of a freshly resolved batch fails to connect.

**Complementary prevention: monitor ZFND's own nodes via `/healthy` + `/ready`**

`zebrad` can serve two opt-in HTTP endpoints (`zebrad/src/components/health.rs`,
disabled by default, enabled with `health.listen_addr`):
- `GET /healthy` — 200 if up and ≥ `min_connected_peers` (not isolated).
- `GET /ready` — 200 if near the chain tip (block lag + tip-age thresholds).

These are the right signal for the `zfnd-{dev,prod}-zebra` nodes that *back* the
seeders (the Corefile `bootstrap_peers`). `/ready` catches something the seeder
prober cannot — a node that handshakes fine but is stale/desynced — and
`/healthy` catches the "our nodes aren't discoverable / isolated" failure from
this incident. Catching that early prevents a seeder from bootstrapping a bad
peer set in the first place.

Recommended (clean) split, to avoid duplicating Kuma:
- These are plain HTTP→200 checks, so monitor them as **native Uptime Kuma HTTP
  monitors** (declared in the Kuma config-as-code), NOT inside the Go prober.
- Keep the prober single-purpose: the Zcash p2p handshake of seeder-returned
  peers is the one thing Kuma can't do; the prober surfaces to Kuma via
  `/healthz`.
- These endpoints are zebrad-only, opt-in, and unauthenticated — they do NOT
  help validate the arbitrary peers a seeder returns (those are random
  operators' nodes); the handshake remains the universal validator there.
- Prerequisite: enable `health.listen_addr` on the ZFND nodes and make the port
  reachable from Kuma over the internal VPC (infra/Gus).

**Future remediation ("auto-disable a bad NS")**
- The intuition that a bad nameserver should be pulled automatically is sound —
  not inside DNS, but as automation on top of the prober: when a backend is
  confirmed DIVERGING across several spaced runs, call the Cloudflare API to
  remove it from the `seeder.zfnd.org` delegation. Needs guardrails (never pull
  below a minimum NS count; require sustained, not single-run, divergence).
