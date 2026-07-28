# Zcash DNS-seeder health prober

Validates that the Zcash DNS seeders return **live, healthy nodes** — not just
that they answer DNS. The old `nightly2` Uptime Kuma monitors only checked
"did the query return ≥1 A record?", which is why the 2026-05-28 near-stall
(seeders serving stale/dead IPs) showed all-green.

## What it does

For each configured seeder it:

1. **Enumerates the zone's authoritative nameservers** by iterative resolution
   from the root (the seeder zones don't serve `NS` at their own apex, so a
   recursive lookup returns nothing — the delegation lives in the parent). For
   `seeder.zfnd.org` this yields `ns1..ns6.zfnd.org`.
2. **Queries each nameserver directly** (recursion disabled) for the seeder's A
   records, so we see *that backend's own* answer, not a recursive resolver's
   cached/arbitrary pick.
3. **Handshakes the returned IPs** with the dnsseeder's own Zcash
   version/verack on the default p2p port (8233 mainnet / 18233 testnet). A peer
   counts as live only if it completes the full handshake at the seeder's own
   protocol-version floor — TCP-reachable is not enough.
4. **Classifies per nameserver** and rolls up to the seeder.

### Why per-nameserver, and why "divergence"

A recursive DNS check hits one arbitrary nameserver and hides which backend is
rotten. The incident was one stale backend (`ns1`) among healthy ones, so the
prober compares the nameservers against each other:

- **`DOWN(divergence)`** — a nameserver's live ratio is far below its siblings'
  median (`< divergence-factor × median`, when the median is healthy). This is
  the real fault signal and the incident signature.
- **`INCONCLUSIVE`** — records returned but absolute liveness is low *with no
  sibling divergence*. Treated as soft, **not** a failure, because a single run
  can land inside a self-induced cooldown (see below). Single-nameserver
  seeders (ECC, str4d) can only ever be INCONCLUSIVE, never DIVERGING.
- **`DOWN(dns)`** — the nameserver didn't answer or returned no records.
- **`DOWN(probe)`** — records resolved across the run and *not one* peer
  handshaked, on any target. This is judged run-wide on purpose: probing a
  single target faster than its cooldown really can drive that target to zero,
  and that is the cadence caveat, not a fault. Handshaking nothing anywhere is
  different — independent operators' peers do not all fall silent at once, so
  the common factor is us. Check the advertised protocol version against the
  seeder's floor first (see [Dependencies](#dependencies)), then whether the
  probe host is rate-limited.
- **`UP`** — records present, no divergence, absolute liveness fine.

Exit code is non-zero only on a hard down (`DOWN(divergence)` / `DOWN(dns)` /
`DOWN(probe)`).

### The cooldown caveat (important for cadence)

The nameservers serve heavily overlapping peer sets, and Zcash nodes
rate-limit rapid repeat connections from one IP. So:

- Within a run we handshake the **union per network exactly once**.
- **Across runs, probe no more often than ~15 minutes.** Probing again within a
  few minutes finds the peers still in cooldown and depresses *every*
  nameserver uniformly — which is exactly why uniform-low is INCONCLUSIVE, not
  DOWN, and why divergence (a *relative* signal) is the trustworthy one.

This matches the seeder's own crawl cadence (15 min testnet / 30 min mainnet).

## Usage

One-shot (prints and exits; non-zero exit on hard-down):

```sh
go run . --config seeders.yaml          # human table
go run . --config seeders.yaml --json    # machine-readable
```

Server mode (probe on an interval, expose results over HTTP):

```sh
go run . --config seeders.yaml --serve :8899 --interval 15m
```

Endpoints:

| route | purpose |
|-------|---------|
| `GET /` | plain-text table (same as CLI) |
| `GET /results` | full JSON snapshot (`generated_at`, `duration_ms`, `results[]`) |
| `GET /healthz` | `200` when no hard-down, `503` + `hard_down[]` otherwise — the endpoint Uptime Kuma would monitor |

The initial probe runs at startup (~60–90s for the handshakes) before serving.

### Reading it with Bruno

A [Bruno](https://www.usebruno.com/) collection is in [`bruno/`](bruno). Open
that folder in the Bruno app, select the **Local** environment (`baseUrl =
http://localhost:8899`), and run `Healthz` / `Results` / `Human`. Each request's
**Docs** tab explains how to interpret the response.

Flags (defaults shown):

| flag | default | meaning |
|------|---------|---------|
| `--config` | `seeders.yaml` | targets file |
| `--json` | false | emit JSON array of `TargetResult` |
| `--divergence-factor` | 0.5 | DIVERGING if ratio < factor × sibling median |
| `--median-healthy-floor` | 0.5 | only judge divergence when sibling median ≥ this |
| `--count-floor` | 5 | absolute soft floor: min live peers |
| `--ratio-floor` | 0.5 | absolute soft floor: min live ratio |
| `--concurrency` | 16 | max concurrent handshakes |
| `--dns-timeout` | 5s | per DNS query |

## Config

See [`seeders.yaml`](seeders.yaml). Each target needs
`hostname` and `network` (`mainnet`/`testnet`); `nameservers:` optionally pins
an explicit list (hostnames or IPs) instead of auto-discovery.

## Dependencies

Reuses the dnsseeder's own handshake so our "healthy" bar matches the seeder's
exactly, and **mirrors its `replace github.com/btcsuite/btcd =>
github.com/ZcashFoundation/btcd`** — without that replace the handshake would
speak Bitcoin, not Zcash.

Pinned to `dnsseeder v0.5.0`, which advertises **170150 (NU6.2)**. What this
pin has to track is the protocol floor of the seeder under test, not whatever
another deployment happens to run: the zeeder fleet serves only peers at its own
floor, so a prober advertising less than that is refused by *every* peer it is
handed.

**Bump this at every network upgrade.** Left on `v0.4.0` (170140, NU6.1) after
NU6.2 raised the floor, the prober returned 0 live peers on all six
nameservers — TCP to `:8233` succeeded, the handshake did not. It was reported
as `INCONCLUSIVE` with `/healthz` still 200, which is why `DOWN(probe)` now
exists to make that shape loud. NU6.3 (Ironwood) raises the floor to 170160 and
will reproduce it.

## Status / deferred

Standalone (logs + JSON + exit code). Not yet wired: Uptime Kuma Push
integration and Cloud Run Job deployment — see the repository plan and
[`../docs/dns-seeder-resilience.md`](../docs/dns-seeder-resilience.md).
