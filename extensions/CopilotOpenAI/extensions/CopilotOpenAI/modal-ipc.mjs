import { mkdir, readFile, unlink, writeFile } from "node:fs/promises";
import { randomBytes } from "node:crypto";
import { join } from "node:path";

const MAX_REQUEST_AGE_MS = 30_000;
const DEFAULT_TIMEOUT_MS = 3_000;
const afterburnerHome = () => process.env.AFTERBURNER_HOME ?? join(process.env.USERPROFILE ?? "", ".afterburner");
const stateDirectory = () => join(afterburnerHome(), "state", "copilot-openai");
const modalActivationPath = () => join(stateDirectory(), "modal-activation.jsonl");
const modalAckDirectory = () => join(stateDirectory(), "modal-activation-acks");
const actionPath = () => join(stateDirectory(), "modal-actions.jsonl");
const actionAckDirectory = () => join(stateDirectory(), "modal-action-acks");
const statePath = () => join(stateDirectory(), "bridge-state.json");

const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
const now = () => new Date().toISOString();

async function appendJsonLine(path, value) {
    await mkdir(join(path, ".."), { recursive: true });
    await writeFile(path, `${JSON.stringify(value)}\n`, { flag: "a" });
}

async function readAndClearJsonLines(path, predicate = () => true) {
    let body = "";
    try { body = await readFile(path, "utf8"); }
    catch (error) {
        if (error?.code === "ENOENT") return [];
        throw error;
    }
    try { await unlink(path); } catch {}
    const current = Date.now();
    return body.split(/\r?\n/).filter(Boolean).map(line => {
        try { return JSON.parse(line); }
        catch { return null; }
    }).filter(request => {
        if (!request || request.schemaVersion !== 1 || typeof request.requestId !== "string") return false;
        const createdAt = Date.parse(request.createdAt ?? "");
        return Number.isFinite(createdAt) && current - createdAt <= MAX_REQUEST_AGE_MS && predicate(request);
    });
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
    try { return JSON.parse(await readFile(statePath(), "utf8")); }
    catch { return { schemaVersion: 1, active: false, endpoint: null, modelCount: 0, requestCount: 0, errorCount: 0, detail: "state unavailable" }; }
}

export async function requestModalOpen(input = {}) {
    const request = {
        schemaVersion: 1,
        requestId: randomBytes(12).toString("hex"),
        surfaceId: "copilot-openai",
        createdAt: now(),
        input
    };
    await mkdir(modalAckDirectory(), { recursive: true });
    await appendJsonLine(modalActivationPath(), request);
    const ack = await waitForAck(modalAckDirectory(), request.requestId, input.timeoutMs ?? DEFAULT_TIMEOUT_MS);
    return { ok: ack.ok === true, requestId: request.requestId, error: ack.error };
}

export async function consumeModalOpenRequests() {
    return readAndClearJsonLines(modalActivationPath(), request => request.surfaceId === "copilot-openai");
}

export async function completeModalOpenRequest(request, result = {}) {
    if (!request?.requestId) return false;
    await mkdir(modalAckDirectory(), { recursive: true });
    await writeFile(join(modalAckDirectory(), `${request.requestId}.json`), JSON.stringify({ schemaVersion: 1, requestId: request.requestId, ok: result.ok === true, error: result.error, completedAt: now() }), "utf8");
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
    return readAndClearJsonLines(actionPath(), request => typeof request.action === "string");
}

export async function completeBridgeActionRequest(request, result = {}) {
    if (!request?.requestId) return false;
    await mkdir(actionAckDirectory(), { recursive: true });
    await writeFile(join(actionAckDirectory(), `${request.requestId}.json`), JSON.stringify({ schemaVersion: 1, requestId: request.requestId, ok: result.ok === true, state: result.state, error: result.error, completedAt: now() }), "utf8");
    return true;
}
