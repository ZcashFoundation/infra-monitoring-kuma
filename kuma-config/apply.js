#!/usr/bin/env node
/*
 * Declaratively apply kuma.yaml to an Uptime Kuma 2.x instance over Socket.IO.
 *
 * Kuma 2.x has no REST/import API and the community python lib doesn't support
 * it, so this speaks the Socket.IO events directly (names verified against the
 * 2.3.2 server: login / add / addNotification / getTags / addTag / addMonitorTag).
 *
 * Idempotent and declarative. Notifications, tags and groups are matched by name
 * and created if missing. Monitors are reconciled: missing ones are created, and
 * existing ones are updated so their declared fields match kuma.yaml, with the
 * declared notifications and tags ensured-present. It is non-destructive — it
 * never removes monitors, channels or tags that aren't in the file — so it stays
 * additive on multi-valued state while keeping the file authoritative on fields.
 * Re-running is safe.
 *
 * Secrets come from the environment, never from the YAML:
 *   KUMA_URL, KUMA_USERNAME, KUMA_PASSWORD  (admin login)
 *   <webhookEnv> per notification           (e.g. SLACK_WEBHOOK_URL)
 *
 * Usage:
 *   node apply.js [--config kuma.yaml] [--dry-run]
 */

const fs = require("node:fs");
const path = require("node:path");
const yaml = require("js-yaml");
const { io } = require("socket.io-client");

const args = process.argv.slice(2);
const DRY_RUN = args.includes("--dry-run");
const configPath = (() => {
    const i = args.indexOf("--config");
    if (i === -1) return path.join(__dirname, "kuma.yaml");
    if (!args[i + 1]) {
        console.error("error: --config requires a path argument");
        process.exit(2);
    }
    return args[i + 1];
})();

const KUMA_URL = requireEnv("KUMA_URL");
const KUMA_USERNAME = requireEnv("KUMA_USERNAME");
const KUMA_PASSWORD = requireEnv("KUMA_PASSWORD");

function requireEnv(name) {
    const v = process.env[name];
    if (!v) {
        console.error(`error: missing required env var ${name}`);
        process.exit(2);
    }
    return v;
}

// Fields the `add` handler JSON-stringifies or requires, plus sensible defaults.
// Merged into every monitor payload (per-monitor YAML overrides win). maxretries
// makes Down require several consecutive failures (not a one-off blip) — that's
// what filters the transient timeouts that fired false alerts. timeout only
// affects HTTP checks (Kuma ignores it for DNS) and is kept < retryInterval so
// each attempt finishes before the next retry.
const MONITOR_DEFAULTS = {
    interval: 60,
    retryInterval: 20,
    resendInterval: 0,
    maxretries: 3,
    timeout: 15,
    method: "GET",
    accepted_statuscodes: ["200-299"],
    conditions: [],
    upsideDown: false,
    expiryNotification: false,
    ignoreTls: false,
    maxredirects: 10,
    packetSize: 56,
    weight: 2000,
    dns_resolve_type: "A",
    dns_resolve_server: "1.1.1.1",
    kafkaProducerBrokers: [],
    kafkaProducerSaslOptions: { mechanism: "None" },
    kafkaProducerSsl: false,
    kafkaProducerAllowAutoTopicCreation: false,
    rabbitmqNodes: [],
    gamedigGivenPortOnly: true,
    mqttCheckType: "keyword",
    oauth_auth_method: "client_secret_basic",
};

function buildSlackNotification(n) {
    const url = process.env[n.webhookEnv]; // presence checked by caller
    return {
        name: n.name,
        type: "slack",
        isDefault: !!n.default,
        // One-time: when the notification is created, attach it to all monitors
        // that already exist (Kuma honors this in Notification.save). Lets you
        // add a channel after monitors are imported and have it wired up.
        applyExisting: !!n.applyExisting,
        slackwebhookURL: url,
        slackchannel: n.channel || "",
        slackusername: n.username || "Uptime Kuma",
        slackiconemo: n.iconEmoji || "",
        slackrichmessage: n.richMessage !== false,
        slackchannelnotify: !!n.channelNotify,
        slackUseTemplate: false,
        slackIncludeGroupName: true,
    };
}

const NOTIFICATION_BUILDERS = { slack: buildSlackNotification };

// expandEnv replaces ${VAR} in the raw config with the environment value, so the
// same kuma.yaml works across instances (e.g. the prober monitor's PROBER_BASE_URL
// differs dev vs prod). Unset vars are left as literals on purpose: a monitor that
// still carries an unexpanded ${VAR} is skipped at creation (see the monitor loop)
// rather than created with an invalid value — this lets the prober monitor be
// added later (Phase 2) without blocking the rest of the apply.
function expandEnv(text) {
    return text.replace(/\$\{(\w+)\}/g, (literal, name) =>
        process.env[name] != null ? process.env[name] : literal,
    );
}

async function main() {
    const cfg = yaml.load(expandEnv(fs.readFileSync(configPath, "utf8")));
    const socket = io(KUMA_URL, {
        transports: ["websocket"],
        reconnection: false,
        timeout: 15000,
    });

    // The server pushes these after a successful login.
    const pushed = { monitorList: {}, notificationList: [] };
    socket.on("monitorList", (l) => {
        pushed.monitorList = l || {};
    });
    socket.on("notificationList", (l) => {
        pushed.notificationList = l || [];
    });

    const emit = (event, ...a) =>
        new Promise((resolve, reject) => {
            socket.emit(event, ...a, (res) => {
                if (res && res.ok === false)
                    reject(new Error(res.msg || `${event} failed`));
                else resolve(res);
            });
        });

    await new Promise((resolve, reject) => {
        socket.once("connect", resolve);
        socket.once("connect_error", (e) =>
            reject(new Error(`connect failed: ${e.message}`)),
        );
    });
    log("connected to", KUMA_URL);

    const loginRes = await emit("login", {
        username: KUMA_USERNAME,
        password: KUMA_PASSWORD,
        token: "",
    });
    if (!loginRes?.ok)
        throw new Error("login failed (check KUMA_USERNAME/KUMA_PASSWORD)");
    log("logged in as", KUMA_USERNAME);

    await sleep(1500); // let the server push monitorList / notificationList

    const existingNotifs = new Map(
        (pushed.notificationList || []).map((n) => [n.name, n.id]),
    );
    const existingMonitors = new Map(
        Object.values(pushed.monitorList || {}).map((m) => [m.name, m]),
    );
    const tagsRes = await emit("getTags");
    const existingTags = new Map((tagsRes?.tags || []).map((t) => [t.name, t]));

    // 1. Notifications
    const notifIds = {};
    for (const n of cfg.notifications || []) {
        if (existingNotifs.has(n.name)) {
            notifIds[n.name] = existingNotifs.get(n.name);
            log(
                `notification "${n.name}" exists (id=${notifIds[n.name]}) — skip`,
            );
            continue;
        }
        if (n.webhookEnv && !process.env[n.webhookEnv]) {
            log(
                `notification "${n.name}": ${n.webhookEnv} not set — skipping it. Monitors are created/reconciled without this channel; set ${n.webhookEnv} and re-run to attach it (reconcile wires it onto existing monitors too).`,
            );
            continue;
        }
        const builder = NOTIFICATION_BUILDERS[n.type];
        if (!builder)
            throw new Error(`unsupported notification type: ${n.type}`);
        const payload = builder(n);
        if (DRY_RUN) {
            log(`[dry-run] create notification "${n.name}" (${n.type})`);
            continue;
        }
        const res = await emit("addNotification", payload, null);
        notifIds[n.name] = res.id;
        log(`created notification "${n.name}" (id=${res.id})`);
    }

    // 2. Tags
    const tagIds = {};
    for (const [name, color] of Object.entries(cfg.tags || {})) {
        if (existingTags.has(name)) {
            tagIds[name] = existingTags.get(name).id;
            log(`tag "${name}" exists (id=${tagIds[name]}) — skip`);
            continue;
        }
        if (DRY_RUN) {
            log(`[dry-run] create tag "${name}" (${color})`);
            continue;
        }
        const res = await emit("addTag", { name, color });
        tagIds[name] = res.tag.id;
        log(`created tag "${name}" (id=${tagIds[name]})`);
    }

    // 3. Groups (monitors of type "group"), then monitors.
    const groupIds = {};
    for (const g of cfg.groups || []) {
        const existing = existingMonitors.get(g);
        if (existing) {
            groupIds[g] = existing.id;
            log(`group "${g}" exists (id=${existing.id}) — skip`);
            continue;
        }
        if (DRY_RUN) {
            log(`[dry-run] create group "${g}"`);
            continue;
        }
        const res = await emit("add", {
            ...MONITOR_DEFAULTS,
            type: "group",
            name: g,
            parent: null,
        });
        groupIds[g] = res.monitorID;
        log(`created group "${g}" (id=${res.monitorID})`);
    }

    // 4. Monitors
    const monitorIds = {}; // name -> id, for status page wiring below
    for (const m of cfg.monitors || []) {
        // Skip monitors whose ${VAR} placeholders weren't resolved (e.g. the
        // prober monitor before PROBER_BASE_URL is set) so we never create a
        // monitor with an invalid value. Set the env var to include it.
        const unresolved = JSON.stringify(m).match(/\$\{\w+\}/);
        if (unresolved) {
            log(`monitor "${m.name}": ${unresolved[0]} not set in env — skip`);
            continue;
        }
        const { group, notifications, tags, ...fields } = m;
        const wantNotifs = {};
        for (const nn of notifications || []) {
            if (notifIds[nn] != null) wantNotifs[notifIds[nn]] = true;
        }
        const declared = {
            ...MONITOR_DEFAULTS,
            ...fields,
            parent: group ? (groupIds[group] ?? null) : null,
            notificationIDList: wantNotifs,
        };

        const existing = existingMonitors.get(m.name);
        let monitorID;
        let existingTags = existing?.tags || [];
        if (existing) {
            // Reconcile in place: declared fields win, notifications are unioned
            // in (never removed). Round-trip the full monitor via getMonitor so we
            // only change what we declare and keep everything else intact. Leaf
            // monitors only — groups never get notifications (avoids double-notify).
            monitorID = existing.id;
            if (DRY_RUN) {
                log(
                    `[dry-run] reconcile monitor "${m.name}" (id=${monitorID})`,
                );
            } else {
                const full =
                    (await emit("getMonitor", monitorID))?.monitor || existing;
                existingTags = full.tags || existingTags;
                await emit("editMonitor", {
                    ...full,
                    ...declared,
                    id: monitorID,
                    notificationIDList: {
                        ...(full.notificationIDList || {}),
                        ...wantNotifs,
                    },
                });
                log(`reconciled monitor "${m.name}" (id=${monitorID})`);
            }
        } else {
            if (DRY_RUN) {
                log(
                    `[dry-run] create monitor "${m.name}" (${m.type}) under "${group || "-"}" tags=${(tags || []).map((t) => `${t.name}:${t.value}`).join(",")}`,
                );
                continue;
            }
            const res = await emit("add", declared);
            monitorID = res.monitorID;
            log(`created monitor "${m.name}" (id=${monitorID})`);
        }
        monitorIds[m.name] = monitorID;

        if (DRY_RUN) continue;

        // Ensure declared tags are present (additive — never removes tags).
        const haveTags = new Set(
            existingTags.map((t) => `${t.name}:${t.value || ""}`),
        );
        for (const t of tags || []) {
            const tagID = tagIds[t.name];
            if (tagID == null) {
                log(`  ! unknown tag "${t.name}" — skip`);
                continue;
            }
            if (haveTags.has(`${t.name}:${t.value || ""}`)) continue;
            await emit("addMonitorTag", tagID, monitorID, t.value || "");
            log(`  + tag ${t.name}=${t.value}`);
        }
    }

    // 5. Status pages (public, no-login views at /status/<slug>). Unlike the
    // monitor reconcile, a page's layout is authoritative: saveStatusPage
    // replaces its groups/monitors with the declared list. Pages not declared
    // here are never touched. Events verified against 2.3.2 and 2.5.0:
    // getStatusPage / addStatusPage / saveStatusPage have identical signatures.
    for (const sp of cfg.statusPages || []) {
        const existing = await emit("getStatusPage", sp.slug).catch(() => null);
        if (DRY_RUN) {
            log(
                `[dry-run] ${existing ? "reconcile" : "create"} status page "${sp.slug}" (${(sp.groups || []).length} groups)`,
            );
            continue;
        }
        if (!existing) {
            await emit("addStatusPage", sp.title, sp.slug);
            log(`created status page "${sp.slug}"`);
        }
        const full = (await emit("getStatusPage", sp.slug)).config;
        const groupList = (sp.groups || []).map((g) => ({
            name: g.name,
            monitorList: (g.monitors || [])
                .filter((name) => {
                    if (monitorIds[name] == null) {
                        log(
                            `  ! status page "${sp.slug}": unknown monitor "${name}" — skip`,
                        );
                        return false;
                    }
                    return true;
                })
                .map((name) => ({ id: monitorIds[name] })),
        }));
        // Round-trip the stored config so undeclared fields survive; icon goes
        // through the imgDataUrl param (non-data: values are stored verbatim).
        await emit(
            "saveStatusPage",
            sp.slug,
            {
                ...full,
                slug: sp.slug,
                title: sp.title,
                description: sp.description ?? full.description ?? null,
                theme: sp.theme || full.theme || "auto",
                showTags: !!sp.showTags,
                showPoweredBy: sp.showPoweredBy ?? full.showPoweredBy ?? false,
                showCertificateExpiry: !!sp.showCertificateExpiry,
            },
            full.icon || "",
            groupList,
        );
        log(`reconciled status page "${sp.slug}" (${groupList.length} groups)`);
    }

    log(DRY_RUN ? "dry-run complete" : "apply complete");
    socket.close();
}

function log(...a) {
    console.log(...a);
}
function sleep(ms) {
    return new Promise((r) => setTimeout(r, ms));
}

main().catch((e) => {
    console.error("error:", e.message);
    process.exit(1);
});
