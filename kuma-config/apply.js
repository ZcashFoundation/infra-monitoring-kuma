#!/usr/bin/env node
/*
 * Declaratively apply kuma.yaml to an Uptime Kuma 2.x instance over Socket.IO.
 *
 * Kuma 2.x has no REST/import API and the community python lib doesn't support
 * it, so this speaks the Socket.IO events directly (names verified against the
 * 2.3.2 server: login / add / addNotification / getTags / addTag / addMonitorTag).
 *
 * Idempotent: notifications, tags, groups, and monitors are matched by name and
 * skipped if they already exist. Re-running is safe.
 *
 * Secrets come from the environment, never from the YAML:
 *   KUMA_URL, KUMA_USERNAME, KUMA_PASSWORD  (admin login)
 *   <webhookEnv> per notification           (e.g. SLACK_WEBHOOK_URL)
 *
 * Usage:
 *   node apply.js [--config kuma.yaml] [--dry-run]
 */

const fs = require("fs");
const path = require("path");
const yaml = require("js-yaml");
const { io } = require("socket.io-client");

const args = process.argv.slice(2);
const DRY_RUN = args.includes("--dry-run");
const configPath = (() => {
    const i = args.indexOf("--config");
    return i >= 0 ? args[i + 1] : path.join(__dirname, "kuma.yaml");
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
// Per-monitor YAML overrides are merged on top of these.
const MONITOR_DEFAULTS = {
    interval: 60,
    retryInterval: 60,
    resendInterval: 0,
    maxretries: 0,
    timeout: 48,
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

async function main() {
    const cfg = yaml.load(fs.readFileSync(configPath, "utf8"));
    const socket = io(KUMA_URL, {
        transports: ["websocket"],
        reconnection: false,
        timeout: 15000,
    });

    // The server pushes these after a successful login.
    const pushed = { monitorList: {}, notificationList: [] };
    socket.on("monitorList", (l) => { pushed.monitorList = l || {}; });
    socket.on("notificationList", (l) => { pushed.notificationList = l || []; });

    const emit = (event, ...a) =>
        new Promise((resolve, reject) => {
            socket.emit(event, ...a, (res) => {
                if (res && res.ok === false) reject(new Error(res.msg || `${event} failed`));
                else resolve(res);
            });
        });

    await new Promise((resolve, reject) => {
        socket.once("connect", resolve);
        socket.once("connect_error", (e) => reject(new Error(`connect failed: ${e.message}`)));
    });
    log("connected to", KUMA_URL);

    const loginRes = await emit("login", { username: KUMA_USERNAME, password: KUMA_PASSWORD, token: "" });
    if (!loginRes || !loginRes.ok) throw new Error("login failed (check KUMA_USERNAME/KUMA_PASSWORD)");
    log("logged in as", KUMA_USERNAME);

    await sleep(1500); // let the server push monitorList / notificationList

    const existingNotifs = new Map((pushed.notificationList || []).map((n) => [n.name, n.id]));
    const existingMonitors = new Map(Object.values(pushed.monitorList || {}).map((m) => [m.name, m]));
    const tagsRes = await emit("getTags");
    const existingTags = new Map(((tagsRes && tagsRes.tags) || []).map((t) => [t.name, t]));

    // 1. Notifications
    const notifIds = {};
    for (const n of cfg.notifications || []) {
        if (existingNotifs.has(n.name)) {
            notifIds[n.name] = existingNotifs.get(n.name);
            log(`notification "${n.name}" exists (id=${notifIds[n.name]}) — skip`);
            continue;
        }
        if (n.webhookEnv && !process.env[n.webhookEnv]) {
            log(`notification "${n.name}": ${n.webhookEnv} not set — skipping for now (monitors still import; re-run after adding the secret)`);
            continue;
        }
        const builder = NOTIFICATION_BUILDERS[n.type];
        if (!builder) throw new Error(`unsupported notification type: ${n.type}`);
        const payload = builder(n);
        if (DRY_RUN) { log(`[dry-run] create notification "${n.name}" (${n.type})`); continue; }
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
        if (DRY_RUN) { log(`[dry-run] create tag "${name}" (${color})`); continue; }
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
        if (DRY_RUN) { log(`[dry-run] create group "${g}"`); continue; }
        const res = await emit("add", { ...MONITOR_DEFAULTS, type: "group", name: g, parent: null });
        groupIds[g] = res.monitorID;
        log(`created group "${g}" (id=${res.monitorID})`);
    }

    // 4. Monitors
    for (const m of cfg.monitors || []) {
        if (existingMonitors.has(m.name)) {
            log(`monitor "${m.name}" exists — skip`);
            continue;
        }
        const { group, notifications, tags, ...fields } = m;
        const notificationIDList = {};
        for (const nn of notifications || []) {
            if (notifIds[nn] != null) notificationIDList[notifIds[nn]] = true;
        }
        const payload = {
            ...MONITOR_DEFAULTS,
            ...fields,
            parent: group ? groupIds[group] ?? null : null,
            notificationIDList,
        };
        if (DRY_RUN) {
            log(`[dry-run] create monitor "${m.name}" (${m.type}) under "${group || "-"}" tags=${(tags || []).map((t) => t.name + ":" + t.value).join(",")}`);
            continue;
        }
        const res = await emit("add", payload);
        const monitorID = res.monitorID;
        log(`created monitor "${m.name}" (id=${monitorID})`);
        for (const t of tags || []) {
            const tagID = tagIds[t.name];
            if (tagID == null) { log(`  ! unknown tag "${t.name}" — skip`); continue; }
            await emit("addMonitorTag", tagID, monitorID, t.value || "");
            log(`  + tag ${t.name}=${t.value}`);
        }
    }

    log(DRY_RUN ? "dry-run complete" : "apply complete");
    socket.close();
}

function log(...a) { console.log(...a); }
function sleep(ms) { return new Promise((r) => setTimeout(r, ms)); }

main().catch((e) => {
    console.error("error:", e.message);
    process.exit(1);
});
