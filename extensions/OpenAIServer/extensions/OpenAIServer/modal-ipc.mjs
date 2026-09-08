import { mkdir, readFile, rename, unlink, writeFile } from "node:fs/promises";
import { randomBytes } from "node:crypto";
import { join } from "node:path";
import { afterburnerHome, CANONICAL_ID, LEGACY_ID } from "./names.mjs";

const MAX_REQUEST_AGE_MS = 30_000;
const DEFAULT_TIMEOUT_MS = 3_000;
const stateDirectory = (id = CANONICAL_ID) => join(afterburnerHome(), "state", id);
const modalActivationPath = (id = CANONICAL_ID) => join(stateDirectory(id), "modal-activation.jsonl");
const modalAckDirectory = (id = CANONICAL_ID) => join(stateDirectory(id), "modal-activation-acks");
const actionPath = (id = CANONICAL_ID) => join(stateDirectory(id), "modal-actions.jsonl");
const actionAckDirectory = (id = CANONICAL_ID) => join(stateDirectory(id), "modal-action-acks");
const statePath = (id = CANONICAL_ID) => join(stateDirectory(id), "bridge-state.json");
const stateIds = [CANONICAL_ID, LEGACY_ID];

const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
const now = () => new Date().toISOString();

async function appendJsonLine(path, value) {
    await mkdir(join(path, ".."), { recursive: true });
    await writeFile(path, `${JSON.stringify(value)}\n`, { flag: "a" });
}

async function readAndClearJsonLines(path, stateId, predicate = () => true) {
    const claimedPath = `${path}.${process.pid}-${randomBytes(6).toString("hex")}.claimed`;
    try {
        await rename(path, claimedPath);
    } catch (error) {
        if (error?.code === "ENOENT") return [];
        throw error;
    }
    let body = "";
    try {
        body = await readFile(claimedPath, "utf8");
    } finally {
        try {
            await unlink(claimedPath);
        } catch (error) {
            if (error?.code !== "ENOENT") throw error;
        }
    }
    const current = Date.now();
    return body.split(/\r?\n/).filter(Boolean).map(line => {
        try { return JSON.parse(line); }
        catch { return null; }
    }).filter(request => {
        if (!request || request.schemaVersion !== 1 || typeof request.requestId !== "string") return false;
        const createdAt = Date.parse(request.createdAt ?? "");
        return Number.isFinite(createdAt) && current - createdAt <= MAX_REQUEST_AGE_MS && predicate(request);
    }).map(request => ({ ...request, __stateId: stateId }));
}

async function waitForAck(directory, requestId, timeoutMs) {
    await mkdir(directory, { recursive: true });
    const path = join(directory, `${requestId}.json`);
    const deadline = Date.now() + timeoutMs;
    while (Date.now() < deadline) {
        try {
            const ack = JSON.parse(await readFile(path, "utf8"));
            try { await unlink(path); } catch {}
            return ack;
        } catch (error) {
            if (error?.code !== "ENOENT") return { ok: false, error: "ack-invalid" };
        }
        await sleep(50);
    }
    return { ok: false, error: "ack-timeout" };
}

export async function writeBridgeState(state = {}, detail = undefined) {
    await mkdir(stateDirectory(), { recursive: true });
    const snapshot = { schemaVersion: 1, updatedAt: now(), detail, ...state };
    await writeFile(statePath(), `${JSON.stringify(snapshot, null, 2)}\n`, "utf8");
    return snapshot;
}

export async function readBridgeState() {
    for (const id of stateIds) {
        try { return JSON.parse(await readFile(statePath(id), "utf8")); }
        catch {}
    }
    return { schemaVersion: 1, active: false, endpoint: null, modelCount: 0, requestCount: 0, errorCount: 0, detail: "state unavailable" };
}

export async function requestModalOpen(input = {}) {
    const request = {
        schemaVersion: 1,
        requestId: randomBytes(12).toString("hex"),
        surfaceId: CANONICAL_ID,
        createdAt: now(),
        input
    };
    await mkdir(modalAckDirectory(), { recursive: true });
    await appendJsonLine(modalActivationPath(), request);
    const ack = await waitForAck(modalAckDirectory(), request.requestId, input.timeoutMs ?? DEFAULT_TIMEOUT_MS);
    return { ok: ack.ok === true, requestId: request.requestId, error: ack.error };
}

export async function consumeModalOpenRequests() {
    const requests = [];
    for (const id of stateIds) {
        requests.push(...await readAndClearJsonLines(modalActivationPath(id), id, request => request.surfaceId === CANONICAL_ID || request.surfaceId === LEGACY_ID));
    }
    return requests;
}

export async function completeModalOpenRequest(request, result = {}) {
    if (!request?.requestId) return false;
    const id = request.__stateId === LEGACY_ID ? LEGACY_ID : CANONICAL_ID;
    await mkdir(modalAckDirectory(id), { recursive: true });
    await writeFile(join(modalAckDirectory(id), `${request.requestId}.json`), JSON.stringify({ schemaVersion: 1, requestId: request.requestId, ok: result.ok === true, error: result.error, completedAt: now() }), "utf8");
    return true;
}

export async function requestBridgeAction(action, input = {}) {
    const request = { schemaVersion: 1, requestId: randomBytes(12).toString("hex"), action, input, createdAt: now() };
    await mkdir(actionAckDirectory(), { recursive: true });
    await appendJsonLine(actionPath(), request);
    const ack = await waitForAck(actionAckDirectory(), request.requestId, input.timeoutMs ?? DEFAULT_TIMEOUT_MS);
    return { ok: ack.ok === true, state: ack.state, error: ack.error };
}

export async function consumeBridgeActionRequests() {
    const requests = [];
    for (const id of stateIds) {
        requests.push(...await readAndClearJsonLines(actionPath(id), id, request => typeof request.action === "string"));
    }
    return requests;
}

export async function completeBridgeActionRequest(request, result = {}) {
    if (!request?.requestId) return false;
    const id = request.__stateId === LEGACY_ID ? LEGACY_ID : CANONICAL_ID;
    await mkdir(actionAckDirectory(id), { recursive: true });
    await writeFile(join(actionAckDirectory(id), `${request.requestId}.json`), JSON.stringify({ schemaVersion: 1, requestId: request.requestId, ok: result.ok === true, state: result.state, error: result.error, completedAt: now() }), "utf8");
    return true;
}
