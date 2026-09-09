import { mkdir } from "node:fs/promises";
import { join } from "node:path";
import { afterburnerHome, CANONICAL_ID } from "./names.mjs";
import { appendRouteRecord, claimRouteRecords, cleanupRouteArtifacts, newRequestId, readRouteState, requireTrustedRoute, routeQueuePath, waitForRouteAck, writeRouteAck, writeRouteState } from "../../shared/route-ipc.mjs";

const MAX_REQUEST_AGE_MS = 30_000;
const CLEANUP_MAX_AGE_MS = 86_400_000;
const DEFAULT_TIMEOUT_MS = 3_000;
const DEFAULT_ACTION_TIMEOUT_MS = 10_000;
const stateDirectory = (id = CANONICAL_ID) => join(afterburnerHome(), "state", id);
const modalActivationPath = (id = CANONICAL_ID) => routeQueuePath(stateDirectory(id), "modal-activation.jsonl");
const modalAckDirectory = (id = CANONICAL_ID) => join(stateDirectory(id), "modal-activation-acks");
const actionPath = (id = CANONICAL_ID) => routeQueuePath(stateDirectory(id), "modal-actions.jsonl");
const actionAckDirectory = (id = CANONICAL_ID) => join(stateDirectory(id), "modal-action-acks");
const statePath = (id = CANONICAL_ID) => join(stateDirectory(id), "bridge-state.json");

function routeOrUnavailable(env = process.env) {
    try { return requireTrustedRoute(env); }
    catch { return ""; }
}

function unavailable(requestId = "") {
    return { ok: false, requestId, error: "trusted-route-unavailable" };
}

async function cleanupState(id = CANONICAL_ID) {
    await cleanupRouteArtifacts(stateDirectory(id), { maxAgeMs: CLEANUP_MAX_AGE_MS }).catch(() => {});
}

export async function writeBridgeState(state = {}, detail = undefined) {
    if (!routeOrUnavailable()) return { schemaVersion: 2, active: false, endpoint: null, modelCount: 0, requestCount: 0, errorCount: 0, detail: "trusted route unavailable" };
    await cleanupState();
    return writeRouteState(statePath(), { detail, ...state });
}

export async function readBridgeState() {
    if (!routeOrUnavailable()) return { schemaVersion: 2, active: false, endpoint: null, modelCount: 0, requestCount: 0, errorCount: 0, detail: "trusted route unavailable" };
    return readRouteState(statePath(), { fallback: { schemaVersion: 2, active: false, endpoint: null, modelCount: 0, requestCount: 0, errorCount: 0, detail: "state unavailable" } });
}

export async function requestModalOpen(input = {}) {
    if (!routeOrUnavailable()) return unavailable();
    await cleanupState();
    await mkdir(modalAckDirectory(), { recursive: true });
    const request = await appendRouteRecord(modalActivationPath(), {
        requestId: newRequestId(),
        surfaceId: CANONICAL_ID,
        input
    });
    const ack = await waitForRouteAck(modalAckDirectory(), request, { timeoutMs: input.timeoutMs ?? DEFAULT_TIMEOUT_MS });
    return { ok: ack.ok === true, requestId: request.requestId, error: ack.error };
}

export async function consumeModalOpenRequests() {
    if (!routeOrUnavailable()) return [];
    await cleanupState();
    return claimRouteRecords(modalActivationPath(), {
        maxRecordAgeMs: MAX_REQUEST_AGE_MS,
        predicate: request => request.surfaceId === CANONICAL_ID
    });
}

export async function completeModalOpenRequest(request, result = {}) {
    if (!routeOrUnavailable() || !request?.requestId) return false;
    return writeRouteAck(modalAckDirectory(), request, result);
}

export async function requestBridgeAction(action, input = {}) {
    if (!routeOrUnavailable()) return unavailable();
    await cleanupState();
    const request = await appendRouteRecord(actionPath(), {
        requestId: newRequestId(),
        surfaceId: CANONICAL_ID,
        action,
        input
    });
    const ack = await waitForRouteAck(actionAckDirectory(), request, { timeoutMs: input.timeoutMs ?? DEFAULT_ACTION_TIMEOUT_MS });
    return { ok: ack.ok === true, state: ack.state, error: ack.error };
}

export async function consumeBridgeActionRequests() {
    if (!routeOrUnavailable()) return [];
    await cleanupState();
    return claimRouteRecords(actionPath(), {
        maxRecordAgeMs: MAX_REQUEST_AGE_MS,
        predicate: request => request.surfaceId === CANONICAL_ID && typeof request.action === "string"
    });
}

export async function completeBridgeActionRequest(request, result = {}) {
    if (!routeOrUnavailable() || !request?.requestId) return false;
    return writeRouteAck(actionAckDirectory(), request, result);
}
