# Uptime Kuma config-as-code

Declarative monitors + notifications for Uptime Kuma **2.x**, applied over the
Socket.IO API. Kuma 2.x has no REST/import and the community Python lib only
supports 1.x, so `apply.js` speaks the Socket.IO events directly (verified
against the 2.3.2 server).

This fills two gaps at once: re-importing the production monitor set and — the
big one — configuring **notifications declaratively** (prod currently has none).

## Files

- **`kuma.yaml`** — desired state: tags, notifications, groups, monitors. No secrets.
- **`apply.js`** — idempotent applier (matches by name; safe to re-run).

## How it's run

It's a **reconciler**, not strictly one-time: edit `kuma.yaml` and re-run when the
monitor/notification set changes. In practice those changes are infrequent and
run **locally/manually** by a team member, so secrets are passed as environment
variables at run time (we keep them in 1Password) — there's no managed secret to
provision for this. The only Secret Manager entry related to Kuma is
`UPTIME_KUMA_DB_PASSWORD`, which the *runtime* needs (separate concern).

```sh
npm install

export KUMA_URL='https://uptime-kuma-feat-version-upgrade-2t7zoff7fq-ue.a.run.app'
export KUMA_USERNAME='admin'
export KUMA_PASSWORD='…'            # from 1Password
export SLACK_WEBHOOK_URL='https://hooks.slack.com/services/…'   # optional; omit to skip Slack
export PROBER_BASE_URL='https://dns-seeder-prober-….run.app'   # this instance's prober URL

node apply.js --dry-run            # preview (connects, logs in, prints planned changes)
node apply.js                      # apply for real
```

- Omitting `SLACK_WEBHOOK_URL` imports the monitors and skips the Slack channel;
  re-run later with it set to add Slack (idempotent — only creates what's missing).
- Re-running never duplicates: notifications, tags, groups, and monitors are
  matched by name.

## Environment variables

| var | required | source | meaning |
|---|---|---|---|
| `KUMA_URL` | yes | — | instance URL |
| `KUMA_USERNAME` | yes | — | admin user (default `admin`) |
| `KUMA_PASSWORD` | yes | 1Password | admin password (login only; not stored anywhere) |
| `SLACK_WEBHOOK_URL` | no | 1Password | Slack incoming webhook; omit to skip Slack |
| `PROBER_BASE_URL` | yes* | — | DNS Seeder Prober Cloud Run URL for this instance (dev vs prod); the prober monitor's `url` expands `${PROBER_BASE_URL}/status`. *Required only because `kuma.yaml` references the prober monitor. |

`kuma.yaml` references a notification's secret by **env var name**
(`webhookEnv: SLACK_WEBHOOK_URL`) — the value is injected at apply time only and
never written to git.

## If we ever automate this in CI

Wire a job that runs `node apply.js` and injects the same env vars from GitHub
Actions secrets (or Secret Manager). That's the point at which a managed secret
would make sense — not for the current local/manual flow.

## Adding channels later

Add another entry under `notifications:` (e.g. PagerDuty/email) with its own
`webhookEnv`, extend `NOTIFICATION_BUILDERS` in `apply.js` with that provider's
field names (from `server/notification-providers/<type>.js`), and re-run. Attach
it to monitors via their `notifications:` list.
