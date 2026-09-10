import { mkdir } from "node:fs/promises";
import { join } from "node:path";
import { afterburnerHome, CANONICAL_ID } from "./names.mjs";
import { appendRouteRecord, claimRouteRecords, cleanupRouteArtifacts, newRequestId, readRouteState, requireTrustedRoute, routeQueuePath, waitForRouteAck, writeRouteAck, writeRouteState } from "../../shared/route-ipc.mjs";

const MAX_REQUEST_AGE_MS = 30_000;
const CLEANUP_MAX_AGE_MS = 86_400_000;
const DEFAULT_TIMEOUT_MS = 3_000;
const DEFAULT_ACTION_TIMEOUT_MS = 10_000;
const stateDirectory = (id = CANONICAL_ID, env = process.env) => join(afterburnerHome(env), "state", id);
const modalActivationPath = (id = CANONICAL_ID, env = process.env) =>
    routeQueuePath(stateDirectory(id, env), "modal-activation.jsonl", { env });
const modalAckDirectory = (id = CANONICAL_ID, env = process.env) =>
    join(stateDirectory(id, env), "modal-activation-acks");
const actionPath = (id = CANONICAL_ID, env = process.env) =>
    routeQueuePath(stateDirectory(id, env), "modal-actions.jsonl", { env });
const actionAckDirectory = (id = CANONICAL_ID, env = process.env) =>
    join(stateDirectory(id, env), "modal-action-acks");
const statePath = (id = CANONICAL_ID, env = process.env) =>
    join(stateDirectory(id, env), "bridge-state.json");

function routeOrUnavailable(env = process.env) {
    try { return requireTrustedRoute(env); }
    catch { return ""; }
}

function unavailable(requestId = "") {
    return { ok: false, requestId, error: "trusted-route-unavailable" };
}

async function cleanupState(id = CANONICAL_ID, env = process.env) {
    await cleanupRouteArtifacts(stateDirectory(id, env), { maxAgeMs: CLEANUP_MAX_AGE_MS }).catch(() => {});
}

export async function writeBridgeState(state = {}, detail = undefined) {
    const env = { ...process.env };
    if (!routeOrUnavailable(env)) return { schemaVersion: 2, active: false, endpoint: null, modelCount: 0, requestCount: 0, errorCount: 0, detail: "trusted route unavailable" };
    await cleanupState(CANONICAL_ID, env);
    return writeRouteState(statePath(CANONICAL_ID, env), { detail, ...state }, { env });
}

export async function readBridgeState() {
    const env = { ...process.env };
    if (!routeOrUnavailable(env)) return { schemaVersion: 2, active: false, endpoint: null, modelCount: 0, requestCount: 0, errorCount: 0, detail: "trusted route unavailable" };
    return readRouteState(statePath(CANONICAL_ID, env), {
        env,
        fallback: { schemaVersion: 2, active: false, endpoint: null, modelCount: 0, requestCount: 0, errorCount: 0, detail: "state unavailable" }
    });
}

export async function requestModalOpen(input = {}) {
    const env = { ...process.env };
    if (!routeOrUnavailable(env)) return unavailable();
    await cleanupState(CANONICAL_ID, env);
    const ackDirectory = modalAckDirectory(CANONICAL_ID, env);
    await mkdir(ackDirectory, { recursive: true });
    const request = await appendRouteRecord(modalActivationPath(CANONICAL_ID, env), {
        requestId: newRequestId(),
        surfaceId: CANONICAL_ID,
        input
    }, { env });
    const ack = await waitForRouteAck(ackDirectory, request, {
        env,
        timeoutMs: input.timeoutMs ?? DEFAULT_TIMEOUT_MS
    });
    return { ok: ack.ok === true, requestId: request.requestId, error: ack.error };
}

export async function consumeModalOpenRequests() {
    const env = { ...process.env };
    if (!routeOrUnavailable(env)) return [];
    await cleanupState(CANONICAL_ID, env);
    return claimRouteRecords(modalActivationPath(CANONICAL_ID, env), {
        env,
        maxRecordAgeMs: MAX_REQUEST_AGE_MS,
        predicate: request => request.surfaceId === CANONICAL_ID
    });
}

export async function completeModalOpenRequest(request, result = {}) {
    const env = { ...process.env };
    if (!routeOrUnavailable(env) || !request?.requestId) return false;
    return writeRouteAck(modalAckDirectory(CANONICAL_ID, env), request, result, { env });
}

export async function requestBridgeAction(action, input = {}) {
    const env = { ...process.env };
    if (!routeOrUnavailable(env)) return unavailable();
    await cleanupState(CANONICAL_ID, env);
    const request = await appendRouteRecord(actionPath(CANONICAL_ID, env), {
        requestId: newRequestId(),
        surfaceId: CANONICAL_ID,
        action,
        input
    }, { env });
    const ack = await waitForRouteAck(actionAckDirectory(CANONICAL_ID, env), request, {
        env,
        timeoutMs: input.timeoutMs ?? DEFAULT_ACTION_TIMEOUT_MS
    });
    return { ok: ack.ok === true, state: ack.state, error: ack.error };
}

export async function consumeBridgeActionRequests() {
    const env = { ...process.env };
    if (!routeOrUnavailable(env)) return [];
    await cleanupState(CANONICAL_ID, env);
    return claimRouteRecords(actionPath(CANONICAL_ID, env), {
        env,
        maxRecordAgeMs: MAX_REQUEST_AGE_MS,
        predicate: request => request.surfaceId === CANONICAL_ID && typeof request.action === "string"
    });
}

export async function completeBridgeActionRequest(request, result = {}) {
    const env = { ...process.env };
    if (!routeOrUnavailable(env) || !request?.requestId) return false;
    return writeRouteAck(actionAckDirectory(CANONICAL_ID, env), request, result, { env });
}
