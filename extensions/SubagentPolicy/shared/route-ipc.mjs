import { createHash, randomBytes } from "node:crypto";
import { appendFile, mkdir, open, readFile, readdir, rename, rm, stat, unlink, writeFile } from "node:fs/promises";
import { dirname, join } from "node:path";

export const ROUTE_ENV = "AFTERBURNER_SESSION_ROUTE";
export const ROUTE_SCHEMA_VERSION = 2;
export const DEFAULT_MAX_RECORD_AGE_MS = 30_000;
export const DEFAULT_CLAIM_LEASE_MS = 2_000;
export const DEFAULT_LOCK_STALE_MS = 30_000;
export const DEFAULT_LOCK_TIMEOUT_MS = 2_000;
export const DEFAULT_ACK_SKEW_MS = 250;
export const DEFAULT_OWNER_HEARTBEAT_MS = 500;
export const DEFAULT_OWNER_STALE_MS = 5_000;
export const DEFAULT_ATOMIC_RENAME_TIMEOUT_MS = 2_000;

const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
const PROCESS_STARTED_AT = new Date(Date.now() - Math.round(process.uptime() * 1000)).toISOString();
const PROCESS_OWNER_ID = `${process.pid}-${Date.parse(PROCESS_STARTED_AT)}-${randomBytes(8).toString("hex")}`;
const ownerHeartbeats = new Map();
const auditedClaimScans = new Set();

export function trustedRoute(env = process.env) {
    const value = typeof env?.[ROUTE_ENV] === "string" ? env[ROUTE_ENV].trim() : "";
    return isValidRoute(value) ? value : "";
}

export function requireTrustedRoute(env = process.env) {
    const routeId = trustedRoute(env);
    if (!routeId) {
        const error = new Error("A trusted Afterburner session route is required for native extension UI IPC.");
        error.code = "afterburner.route_required";
        throw error;
    }
    return routeId;
}

export function isValidRoute(value) {
    return typeof value === "string" && /^[A-Za-z0-9_-]{32,128}$/.test(value);
}

export function routeDirectoryName(routeId) {
    if (!isValidRoute(routeId)) throw new Error("Invalid route id.");
    return createHash("sha256").update(routeId).digest("base64url").slice(0, 32);
}

export function routeOwnedDirectory(baseDirectory, routeId) {
    return join(baseDirectory, "routes", routeDirectoryName(routeId));
}

export function routeQueuePath(baseDirectory, fileName, options = {}) {
    const routeId = requireTrustedRoute(options.env);
    if (!/^[A-Za-z0-9_.-]+$/.test(fileName)) throw new Error("Invalid route queue file name.");
    return join(routeOwnedDirectory(baseDirectory, routeId), fileName);
}

export function safeRequestId(value) {
    return typeof value === "string" && /^[A-Za-z0-9_-]{8,96}$/.test(value) ? value : "";
}

export function newRequestId() {
    return randomBytes(12).toString("hex");
}

function processIsAlive(pid) {
    if (!Number.isInteger(pid) || pid <= 0) return null;
    try {
        process.kill(pid, 0);
        return true;
    } catch (error) {
        if (error?.code === "ESRCH") return false;
        if (error?.code === "EPERM") return true;
        return null;
    }
}

async function fsyncDirectory(path) {
    let handle;
    try {
        handle = await open(path, "r");
        await handle.sync();
    } catch {
    } finally {
        await handle?.close?.().catch(() => {});
    }
}

export async function atomicWriteFile(path, data, options = {}) {
    await mkdir(dirname(path), { recursive: true });
    const temporary = join(dirname(path), "." + process.pid + "-" + Date.now() + "-" + randomBytes(4).toString("hex") + ".tmp");
    const handle = await open(temporary, "wx");
    try {
        await handle.writeFile(data, "utf8");
        await handle.sync();
    } finally {
        await handle.close();
    }
    const renameFile = options.renameFile ?? rename;
    const deadline = Date.now() + (options.renameTimeoutMs ?? DEFAULT_ATOMIC_RENAME_TIMEOUT_MS);
    const retryMs = options.renameRetryMs ?? 5;
    let renamed = false;
    try {
        while (true) {
            try {
                await renameFile(temporary, path);
                renamed = true;
                break;
            } catch (error) {
                if (!["EPERM", "EACCES", "EBUSY"].includes(error?.code) || Date.now() >= deadline) throw error;
                await sleep(retryMs);
            }
        }
    } finally {
        if (!renamed) await rm(temporary, { force: true }).catch(() => {});
    }
    await fsyncDirectory(dirname(path));
}

export async function withQueueLock(path, operation, options = {}) {
    const lockPath = path + ".lock";
    const deadline = Date.now() + (options.lockTimeoutMs ?? DEFAULT_LOCK_TIMEOUT_MS);
    const staleMs = options.lockStaleMs ?? DEFAULT_LOCK_STALE_MS;
    await mkdir(dirname(path), { recursive: true });
    while (true) {
        let lock;
        try {
            lock = await open(lockPath, "wx");
            try {
                await lock.writeFile(JSON.stringify({ pid: process.pid, acquiredAt: Date.now() }), "utf8");
                await lock.sync?.();
                return await operation();
            } finally {
                await lock.close().catch(() => {});
                await unlink(lockPath).catch(error => {
                    if (error?.code !== "ENOENT") throw error;
                });
            }
        } catch (error) {
            if (error?.code !== "EEXIST" && error?.code !== "EPERM") throw error;
            try {
                const info = await stat(lockPath);
                let ownerAlive = null;
                try { ownerAlive = processIsAlive(JSON.parse(await readFile(lockPath, "utf8"))?.pid); }
                catch {}
                if (ownerAlive === false || Date.now() - info.mtimeMs > staleMs) {
                    await unlink(lockPath).catch(lockError => {
                        if (lockError?.code !== "ENOENT") throw lockError;
                    });
                    continue;
                }
            } catch (statError) {
                if (statError?.code === "ENOENT") {
                    if (Date.now() >= deadline) throw new Error("queue lock timeout: " + path);
                    await sleep(5);
                    continue;
                }
                throw statError;
            }
            if (Date.now() >= deadline) throw new Error("queue lock timeout: " + path);
            await sleep(5);
        }
    }
}

export async function appendRouteRecord(path, record, options = {}) {
    const routeId = requireTrustedRoute(options.env);
    const routeRecord = {
        schemaVersion: ROUTE_SCHEMA_VERSION,
        routeId,
        createdAt: new Date().toISOString(),
        ...record,
        routeId,
        schemaVersion: ROUTE_SCHEMA_VERSION
    };
    await withQueueLock(path, async () => writeFile(path, JSON.stringify(routeRecord) + "\n", { flag: "a" }), options);
    await auditRouteOperation(options, "queue-append", routeId, routeRecord);
    return routeRecord;
}

function classifyRecord(line, routeId, predicate, nowMs, options) {
    let record;
    try { record = JSON.parse(line); }
    catch { return { kind: "malformed" }; }
    if (!record || record.schemaVersion !== ROUTE_SCHEMA_VERSION || !safeRequestId(record.requestId) || !isValidRoute(record.routeId)) {
        return { kind: "malformed" };
    }
    const createdAt = Date.parse(record.createdAt ?? "");
    if (!Number.isFinite(createdAt) || nowMs - createdAt > (options.maxRecordAgeMs ?? DEFAULT_MAX_RECORD_AGE_MS)) {
        return { kind: "stale" };
    }
    if (record.routeId !== routeId || !predicate(record)) return { kind: "preserve", record };
    return { kind: "claim", record };
}

function claimDirectoryFor(path, options = {}) {
    return options.claimDirectory ?? path + ".claimed";
}

function withClaimMetadata(record, claimPath) {
    return Object.defineProperty({ ...record }, "__claimPath", { value: claimPath, enumerable: false });
}

function routeHash(routeId) {
    return createHash("sha256").update(routeId).digest("base64url").slice(0, 16);
}

function auditDirectory(options = {}) {
    const value = options.auditDirectory ?? options.env?.AFTERBURNER_ROUTE_IPC_AUDIT ?? process.env.AFTERBURNER_ROUTE_IPC_AUDIT;
    return typeof value === "string" && value.trim() ? value : "";
}

async function auditRouteOperation(options, operation, routeId, details = {}) {
    const directory = auditDirectory(options);
    if (!directory) return;
    const owner = currentClaimantIdentity();
    const entry = {
        schemaVersion: 1,
        at: new Date().toISOString(),
        operation,
        routeHash: routeHash(routeId),
        requestId: safeRequestId(details.requestId) || undefined,
        surfaceId: typeof details.surfaceId === "string" ? details.surfaceId.slice(0, 96) : undefined,
        action: typeof details.action === "string" ? details.action.slice(0, 64) : undefined,
        ownerPid: process.pid,
        ownerId: owner.ownerId,
        ownerLabel: String(options.env?.AFTERBURNER_ROUTE_IPC_LABEL ?? options.env?.AFTERBURNER_TEST_SESSION_LABEL ?? process.env.AFTERBURNER_ROUTE_IPC_LABEL ?? process.env.AFTERBURNER_TEST_SESSION_LABEL ?? "").slice(0, 64) || undefined
    };
    await mkdir(directory, { recursive: true }).catch(() => {});
    await appendFile(join(directory, `${process.pid}.jsonl`), JSON.stringify(entry) + "\n", "utf8").catch(() => {});
}

async function auditClaimScanOnce(options, routeId, path) {
    if (!auditDirectory(options)) return;
    const key = `${routeId}:${path}`;
    if (auditedClaimScans.has(key)) return;
    auditedClaimScans.add(key);
    await auditRouteOperation(options, "claim-scan", routeId, {});
}

export function currentClaimantIdentity() {
    return { ownerId: PROCESS_OWNER_ID, pid: process.pid, processStartedAt: PROCESS_STARTED_AT };
}

function ownerDirectoryFor(claimDirectory) {
    return join(claimDirectory, ".owners");
}

function ownerPathFor(claimDirectory, ownerId = PROCESS_OWNER_ID) {
    return join(ownerDirectoryFor(claimDirectory), `${ownerId}.json`);
}

async function writeOwnerHeartbeat(claimDirectory, nowMs = Date.now()) {
    const owner = { schemaVersion: ROUTE_SCHEMA_VERSION, ...currentClaimantIdentity(), heartbeatAt: new Date(nowMs).toISOString() };
    await atomicWriteFile(ownerPathFor(claimDirectory), JSON.stringify(owner) + "\n");
    return owner;
}

async function ensureOwnerHeartbeat(claimDirectory, options = {}, nowMs = Date.now()) {
    const owner = await writeOwnerHeartbeat(claimDirectory, nowMs);
    if (options.disableOwnerHeartbeat === true || ownerHeartbeats.has(claimDirectory)) return owner;
    const intervalMs = options.ownerHeartbeatMs ?? DEFAULT_OWNER_HEARTBEAT_MS;
    const timer = setInterval(() => { void writeOwnerHeartbeat(claimDirectory).catch(() => {}); }, Math.max(50, intervalMs));
    timer.unref?.();
    ownerHeartbeats.set(claimDirectory, timer);
    return owner;
}

function validOwner(owner) {
    return owner && typeof owner.ownerId === "string" && /^[A-Za-z0-9_.-]{8,160}$/.test(owner.ownerId) && Number.isInteger(owner.pid) && owner.pid > 0 && typeof owner.processStartedAt === "string";
}

async function claimOwnerIsLive(claimDirectory, claim, nowMs, options = {}) {
    const owner = claim?.owner;
    if (!validOwner(owner)) return false;
    let heartbeat;
    try { heartbeat = JSON.parse(await readFile(ownerPathFor(claimDirectory, owner.ownerId), "utf8")); }
    catch { return false; }
    if (heartbeat?.ownerId !== owner.ownerId || heartbeat?.pid !== owner.pid || heartbeat?.processStartedAt !== owner.processStartedAt) return false;
    const heartbeatAt = Date.parse(heartbeat?.heartbeatAt ?? "");
    const staleMs = options.ownerStaleMs ?? Math.max(DEFAULT_OWNER_STALE_MS, (options.recoverUnackedMs ?? DEFAULT_CLAIM_LEASE_MS) * 3);
    return Number.isFinite(heartbeatAt) && nowMs - heartbeatAt <= staleMs;
}

async function writeDiagnostics(path, diagnostics) {
    if (!diagnostics || (diagnostics.malformed === 0 && diagnostics.stale === 0 && diagnostics.expiredClaims === 0)) return;
    await atomicWriteFile(path + ".diagnostics.json", JSON.stringify({ schemaVersion: 1, updatedAt: new Date().toISOString(), ...diagnostics }) + "\n").catch(() => {});
}

async function recoverClaimedRecords(claimDirectory, routeId, predicate, nowMs, options) {
    const recovered = [];
    const claimedRequestIds = new Set();
    const diagnostics = { malformed: 0, stale: 0, expiredClaims: 0 };
    const recoverAfterMs = options.recoverUnackedMs ?? DEFAULT_CLAIM_LEASE_MS;
    const maxAgeMs = options.maxRecordAgeMs ?? DEFAULT_MAX_RECORD_AGE_MS;
    let entries;
    try { entries = await readdir(claimDirectory, { withFileTypes: true }); }
    catch (error) {
        if (error?.code === "ENOENT") return { recovered, claimedRequestIds, diagnostics };
        throw error;
    }
    for (const entry of entries) {
        if (!entry.isFile() || !entry.name.endsWith(".json")) continue;
        const claimPath = join(claimDirectory, entry.name);
        let claim;
        try { claim = JSON.parse(await readFile(claimPath, "utf8")); }
        catch { continue; }
        const record = claim?.record;
        const claimedAt = Date.parse(claim?.claimedAt ?? "");
        const createdAt = Date.parse(record?.createdAt ?? "");
        if (!record || record.schemaVersion !== ROUTE_SCHEMA_VERSION || record.routeId !== routeId || !safeRequestId(record.requestId) || !predicate(record)) continue;
        claimedRequestIds.add(record.requestId);
        if (!Number.isFinite(createdAt) || nowMs - createdAt > maxAgeMs) {
            await unlink(claimPath).catch(() => {});
            claimedRequestIds.delete(record.requestId);
            diagnostics.expiredClaims++;
            continue;
        }
        if (Number.isFinite(claimedAt) && nowMs - claimedAt < recoverAfterMs) continue;
        if (await claimOwnerIsLive(claimDirectory, claim, nowMs, options)) continue;
        const owner = await ensureOwnerHeartbeat(claimDirectory, options, nowMs);
        const refreshed = { schemaVersion: ROUTE_SCHEMA_VERSION, routeId, requestId: record.requestId, claimedAt: new Date(nowMs).toISOString(), owner, pid: process.pid, record };
        await atomicWriteFile(claimPath, JSON.stringify(refreshed) + "\n");
        await auditRouteOperation(options, "claim-recovered", routeId, record);
        recovered.push(withClaimMetadata(record, claimPath));
    }
    return { recovered, claimedRequestIds, diagnostics };
}

export async function claimRouteRecords(path, options = {}) {
    const routeId = requireTrustedRoute(options.env);
    const predicate = typeof options.predicate === "function" ? options.predicate : () => true;
    const claimDirectory = claimDirectoryFor(path, options);
    return withQueueLock(path, async () => {
        await auditClaimScanOnce(options, routeId, path);
        await mkdir(claimDirectory, { recursive: true });
        const nowMs = Date.now();
        const { recovered, claimedRequestIds, diagnostics } = await recoverClaimedRecords(claimDirectory, routeId, predicate, nowMs, options);
        const owner = await ensureOwnerHeartbeat(claimDirectory, options, nowMs);
        let body = "";
        try { body = await readFile(path, "utf8"); }
        catch (error) {
            if (error?.code === "ENOENT") {
                await writeDiagnostics(path, diagnostics);
                return recovered;
            }
            throw error;
        }
        const lines = body.split(/\r?\n/);
        const preservePartial = body && !/\r?\n$/.test(body) ? lines.pop() : "";
        const claimed = [...recovered];
        const preserved = [];
        diagnostics.malformed = 0;
        diagnostics.stale = 0;
        for (const line of lines) {
            if (!line) continue;
            const result = classifyRecord(line, routeId, predicate, nowMs, options);
            if (result.kind === "claim") {
                if (claimedRequestIds.has(result.record.requestId)) continue;
                const claimPath = join(claimDirectory, result.record.requestId + ".json");
                await atomicWriteFile(claimPath, JSON.stringify({ schemaVersion: ROUTE_SCHEMA_VERSION, routeId, requestId: result.record.requestId, claimedAt: new Date(nowMs).toISOString(), owner, pid: process.pid, record: result.record }) + "\n");
                claimedRequestIds.add(result.record.requestId);
                await auditRouteOperation(options, "claim-new", routeId, result.record);
                claimed.push(withClaimMetadata(result.record, claimPath));
            } else if (result.kind === "preserve") preserved.push(JSON.stringify(result.record));
            else diagnostics[result.kind]++;
        }
        if (preservePartial) preserved.push(preservePartial);
        const next = preserved.length > 0 ? preserved.join("\n") + "\n" : "";
        if (next) await atomicWriteFile(path, next);
        else await unlink(path).catch(error => { if (error?.code !== "ENOENT") throw error; });
        await writeDiagnostics(path, diagnostics);
        return claimed;
    }, options);
}

export function routeAckDirectory(baseDirectory, routeId) {
    return join(baseDirectory, routeDirectoryName(routeId));
}

export async function writeRouteAck(baseDirectory, request, result = {}, options = {}) {
    const routeId = requireTrustedRoute(options.env);
    if (!request || request.routeId !== routeId || !safeRequestId(request.requestId)) return false;
    const directory = routeAckDirectory(baseDirectory, routeId);
    await mkdir(directory, { recursive: true });
    const ack = {
        schemaVersion: ROUTE_SCHEMA_VERSION,
        routeId,
        requestId: request.requestId,
        surfaceId: request.surfaceId,
        ok: result.ok === true,
        error: result.error ? String(result.error).slice(0, 160) : undefined,
        state: result.state,
        completedAt: new Date().toISOString()
    };
    await atomicWriteFile(join(directory, request.requestId + ".json"), JSON.stringify(ack) + "\n");
    await auditRouteOperation(options, "ack-write", routeId, request);
    const claimPath = request.__claimPath ?? (options.claimDirectory ? join(options.claimDirectory, request.requestId + ".json") : undefined);
    if (claimPath) await unlink(claimPath).catch(error => { if (error?.code !== "ENOENT") throw error; });
    return true;
}

function ackIsFresh(ack, request, deadline, options = {}) {
    const completedAt = Date.parse(ack?.completedAt ?? "");
    const createdAt = Date.parse(request?.createdAt ?? "");
    if (!Number.isFinite(completedAt) || !Number.isFinite(createdAt)) return false;
    const skewMs = options.ackSkewMs ?? DEFAULT_ACK_SKEW_MS;
    return completedAt + skewMs >= createdAt && completedAt <= deadline + skewMs;
}

export async function waitForRouteAck(baseDirectory, request, options = {}) {
    const routeId = requireTrustedRoute(options.env);
    if (!request || request.routeId !== routeId || !safeRequestId(request.requestId)) return { ok: false, error: "ack-route-mismatch" };
    const directory = routeAckDirectory(baseDirectory, routeId);
    await mkdir(directory, { recursive: true });
    const path = join(directory, request.requestId + ".json");
    const deadline = Date.now() + (options.timeoutMs ?? 3_000);
    while (Date.now() < deadline) {
        try {
            const ack = JSON.parse(await readFile(path, "utf8"));
            if (ack?.schemaVersion !== ROUTE_SCHEMA_VERSION || ack.routeId !== routeId || ack.requestId !== request.requestId || ack.surfaceId !== request.surfaceId) {
                return { ok: false, error: "ack-route-mismatch" };
            }
            if (!ackIsFresh(ack, request, deadline, options)) {
                await unlink(path).catch(() => {});
            } else {
                await unlink(path).catch(() => {});
                return ack;
            }
        } catch (error) {
            if (error?.code !== "ENOENT") {
            }
        }
        if (typeof options.waitForChange === "function") {
            await options.waitForChange(directory, request.requestId + ".json", Math.min(100, Math.max(1, deadline - Date.now())));
        } else {
            await sleep(Math.min(50, Math.max(1, deadline - Date.now())));
        }
    }
    return { ok: false, error: "ack-timeout" };
}

export async function writeRouteState(path, state = {}, options = {}) {
    const routeId = requireTrustedRoute(options.env);
    const directory = join(dirname(path), "routes", routeDirectoryName(routeId));
    await mkdir(directory, { recursive: true });
    const { routeId: _ignoredRouteId, ...safeState } = state ?? {};
    const snapshot = { schemaVersion: ROUTE_SCHEMA_VERSION, updatedAt: new Date().toISOString(), ...safeState };
    await atomicWriteFile(join(directory, "bridge-state.json"), JSON.stringify(snapshot, null, 2) + "\n");
    await auditRouteOperation(options, "state-write", routeId, {});
    return snapshot;
}

export async function readRouteState(path, options = {}) {
    const routeId = requireTrustedRoute(options.env);
    await auditRouteOperation(options, "state-read", routeId, {});
    try {
        const { routeId: _ignoredRouteId, ...snapshot } = JSON.parse(
            await readFile(join(dirname(path), "routes", routeDirectoryName(routeId), "bridge-state.json"), "utf8")
        );
        return snapshot;
    } catch {
        const { routeId: _ignoredRouteId, ...fallback } = options.fallback ?? {};
        return Object.keys(fallback).length > 0
            ? fallback
            : { schemaVersion: ROUTE_SCHEMA_VERSION, active: false, endpoint: null, modelCount: 0, requestCount: 0, errorCount: 0, detail: "state unavailable" };
    }
}

export async function cleanupRouteArtifacts(baseDirectory, options = {}) {
    const maxAgeMs = options.maxAgeMs ?? 86_400_000;
    const cutoff = Date.now() - maxAgeMs;
    const quarantineMalformed = options.quarantineMalformed === true;
    let removed = 0;
    let quarantined = 0;
    async function visit(directory) {
        let entries;
        try { entries = await readdir(directory, { withFileTypes: true }); }
        catch { return; }
        for (const entry of entries) {
            const path = join(directory, entry.name);
            if (entry.isDirectory()) {
                await visit(path);
                try { await rm(path, { recursive: false }); } catch {}
                continue;
            }
            try {
                const info = await stat(path);
                if (info.mtimeMs >= cutoff) continue;
                if (/\.(?:tmp|lock|claimed)$/i.test(entry.name) || /\.json(?:l)?$/i.test(entry.name)) {
                    if (quarantineMalformed && /\.jsonl?$/i.test(entry.name)) {
                        await rename(path, path + ".expired").catch(async () => { await unlink(path); });
                        quarantined++;
                    } else {
                        await unlink(path);
                        removed++;
                    }
                }
            } catch {}
        }
    }
    await visit(baseDirectory);
    return { removed, quarantined };
}
