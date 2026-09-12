import { mkdir } from "node:fs/promises";
import { dirname, join } from "node:path";
import { appendRouteRecord, claimRouteRecords, cleanupRouteArtifacts, newRequestId, readRouteState, requireTrustedRoute, routeQueuePath, waitForRouteAck, writeRouteAck, writeRouteState } from "../../shared/route-ipc.mjs";
import { CANONICAL_ID } from "./names.mjs";

const home = env => env.AFTERBURNER_HOME ?? join(env.USERPROFILE ?? env.HOME, ".afterburner");
const root = env => join(home(env), "state", CANONICAL_ID);
const openPath = env => routeQueuePath(root(env), "modal-activation.jsonl", { env });
const openAcks = env => join(root(env), "modal-activation-acks");
const actionPath = env => routeQueuePath(root(env), "modal-actions.jsonl", { env });
const actionAcks = env => join(root(env), "modal-action-acks");
const statePath = env => join(root(env), "policy-state.json");
const trusted = env => { try { return Boolean(requireTrustedRoute(env)); } catch { return false; } };
const cleanup = env => cleanupRouteArtifacts(root(env), { maxAgeMs: 86_400_000 }).catch(() => {});

export async function writePolicyState(state) {
    const env = { ...process.env };
    if (!trusted(env)) return { activePolicy: null, detail: "trusted route unavailable" };
    await cleanup(env);
    return writeRouteState(statePath(env), state, { env });
}

export async function readPolicyState() {
    const env = { ...process.env };
    if (!trusted(env)) return { activePolicy: null, policies: {}, detail: "trusted route unavailable" };
    return readRouteState(statePath(env), { env, fallback: { activePolicy: null, policies: {}, detail: "state unavailable" } });
}

export async function requestModalOpen(input = {}) {
    const env = { ...process.env };
    if (!trusted(env)) return { ok: false, error: "trusted-route-unavailable" };
    await cleanup(env);
    const request = await appendRouteRecord(openPath(env), { requestId: newRequestId(), surfaceId: CANONICAL_ID, input }, { env });
    const ack = await waitForRouteAck(openAcks(env), request, { env, timeoutMs: 3000 });
    return { ok: ack.ok === true, error: ack.error };
}

export async function consumeModalOpenRequests() {
    const env = { ...process.env };
    if (!trusted(env)) return [];
    return claimRouteRecords(openPath(env), { env, maxRecordAgeMs: 30_000, predicate: item => item.surfaceId === CANONICAL_ID });
}

export async function completeModalOpenRequest(request, result) {
    const env = { ...process.env };
    return trusted(env) && writeRouteAck(openAcks(env), request, result, { env });
}

export async function requestPolicyAction(action) {
    const env = { ...process.env };
    if (!trusted(env)) return { ok: false, error: "trusted-route-unavailable" };
    await mkdir(dirname(actionPath(env)), { recursive: true });
    const request = await appendRouteRecord(actionPath(env), { requestId: newRequestId(), surfaceId: CANONICAL_ID, action }, { env });
    const ack = await waitForRouteAck(actionAcks(env), request, { env, timeoutMs: 10_000 });
    return { ok: ack.ok === true, state: ack.state, error: ack.error };
}

export async function consumePolicyActions() {
    const env = { ...process.env };
    if (!trusted(env)) return [];
    return claimRouteRecords(actionPath(env), { env, maxRecordAgeMs: 30_000, predicate: item => item.surfaceId === CANONICAL_ID });
}

export async function completePolicyAction(request, result) {
    const env = { ...process.env };
    return trusted(env) && writeRouteAck(actionAcks(env), request, result, { env });
}
