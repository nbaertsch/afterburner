import assert from "node:assert/strict";
import { createHash, randomBytes } from "node:crypto";
import { mkdir, readFile, readdir, rename, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { test } from "node:test";
import {
  appendRouteRecord,
  atomicWriteFile,
  claimRouteRecords,
  currentClaimantIdentity,
  readRouteState,
  routeDirectoryName,
  routeQueuePath,
  writeRouteAck,
  writeRouteState
} from "../extensions/BlackBox/shared/route-ipc.mjs";

const ROUTE_A = "routeAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA";
const ROUTE_B = "routeBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB";
const delay = ms => new Promise(resolve => setTimeout(resolve, ms));

function work(name) {
  return join(tmpdir(), `afterburner-route-ipc-${name}-${process.pid}-${randomBytes(4).toString("hex")}`);
}

function env(route, auditDirectory, label) {
  return { AFTERBURNER_SESSION_ROUTE: route, AFTERBURNER_ROUTE_IPC_AUDIT: auditDirectory, AFTERBURNER_TEST_SESSION_LABEL: label };
}

async function readAudit(directory) {
  const entries = [];
  for (const file of await readdir(directory).catch(() => [])) {
    if (!file.endsWith(".jsonl")) continue;
    const body = await readFile(join(directory, file), "utf8");
    entries.push(...body.trim().split(/\r?\n/).filter(Boolean).map(line => JSON.parse(line)));
  }
  return entries;
}

function staleOwner(id = "old-owner") {
  return { ownerId: id, pid: process.pid, processStartedAt: "2000-01-01T00:00:00.000Z", heartbeatAt: "2000-01-01T00:00:00.000Z" };
}

async function writeStaleClaim(claimDirectory, record, owner = staleOwner(record.requestId)) {
  await mkdir(claimDirectory, { recursive: true });
  await atomicWriteFile(join(claimDirectory, `${record.requestId}.json`), JSON.stringify({
    schemaVersion: 2,
    routeId: record.routeId,
    requestId: record.requestId,
    claimedAt: "2000-01-01T00:00:00.000Z",
    owner,
    pid: owner.pid,
    record
  }) + "\n");
}

test("route IPC helper copies stay identical", async () => {
  const blackBox = await readFile(new URL("../extensions/BlackBox/shared/route-ipc.mjs", import.meta.url), "utf8");
  const openAI = await readFile(new URL("../extensions/OpenAIServer/shared/route-ipc.mjs", import.meta.url), "utf8");
  assert.equal(openAI, blackBox);
});

test("atomic writes retry transient Windows rename contention", async () => {
  const root = work("atomic-rename");
  const path = join(root, "state.json");
  let attempts = 0;
  await atomicWriteFile(path, "ready\n", {
    renameTimeoutMs: 100,
    renameRetryMs: 1,
    renameFile: async (source, target) => {
      attempts++;
      if (attempts < 3) {
        const error = new Error("simulated Windows rename contention");
        error.code = "EPERM";
        throw error;
      }
      await rename(source, target);
    }
  });
  assert.equal(attempts, 3);
  assert.equal(await readFile(path, "utf8"), "ready\n");
});

test("stale claim and queue duplicate are emitted once while other routes survive", async () => {
  const root = work("dedupe");
  const queue = routeQueuePath(root, "modal-actions.jsonl", { env: env(ROUTE_A) });
  const claimDirectory = `${queue}.claimed`;
  const createdAt = new Date().toISOString();
  const duplicate = { schemaVersion: 2, routeId: ROUTE_A, requestId: "duplicate1", surfaceId: "openai-server", action: "status", createdAt };
  const other = { schemaVersion: 2, routeId: ROUTE_B, requestId: "otheroute1", surfaceId: "openai-server", action: "doctor", createdAt };
  await mkdir(join(root, "routes", routeDirectoryName(ROUTE_A)), { recursive: true });
  await writeStaleClaim(claimDirectory, duplicate);
  await writeFile(queue, `${JSON.stringify(duplicate)}\n${JSON.stringify(other)}\n`, "utf8");

  const claimed = await claimRouteRecords(queue, { env: env(ROUTE_A), claimDirectory, recoverUnackedMs: 1, ownerStaleMs: 1, predicate: r => r.surfaceId === "openai-server" });
  assert.deepEqual(claimed.map(r => r.requestId), [duplicate.requestId]);
  assert.match(await readFile(queue, "utf8"), /otheroute1/);
  assert.doesNotMatch(await readFile(queue, "utf8"), /duplicate1/);
});

test("slow live claimant heartbeat prevents lease theft", async () => {
  const root = work("live");
  const queue = routeQueuePath(root, "modal-actions.jsonl", { env: env(ROUTE_A) });
  const request = await appendRouteRecord(queue, { requestId: "liveclaim1", surfaceId: "openai-server", action: "status" }, { env: env(ROUTE_A) });
  const first = await claimRouteRecords(queue, { env: env(ROUTE_A), recoverUnackedMs: 20, ownerHeartbeatMs: 10, ownerStaleMs: 100, predicate: r => r.surfaceId === "openai-server" });
  assert.equal(first[0]?.requestId, request.requestId);
  await delay(80);
  assert.deepEqual(await claimRouteRecords(queue, { env: env(ROUTE_A), recoverUnackedMs: 20, ownerHeartbeatMs: 10, ownerStaleMs: 100, predicate: r => r.surfaceId === "openai-server" }), []);
});

test("dead claimant and stale PID discriminator are recovered", async () => {
  const root = work("dead");
  const queue = routeQueuePath(root, "modal-actions.jsonl", { env: env(ROUTE_A) });
  const claimDirectory = `${queue}.claimed`;
  const createdAt = new Date().toISOString();
  const dead = { schemaVersion: 2, routeId: ROUTE_A, requestId: "deadclaim", surfaceId: "openai-server", action: "status", createdAt };
  const reusedPid = { schemaVersion: 2, routeId: ROUTE_A, requestId: "reusedpid", surfaceId: "openai-server", action: "doctor", createdAt };
  await writeStaleClaim(claimDirectory, dead, { ownerId: "dead-owner", pid: 2147483647, processStartedAt: "2000-01-01T00:00:00.000Z", heartbeatAt: "2000-01-01T00:00:00.000Z" });
  await writeStaleClaim(claimDirectory, reusedPid, staleOwner("same-pid-old-start"));
  const recovered = await claimRouteRecords(queue, { env: env(ROUTE_A), claimDirectory, recoverUnackedMs: 1, ownerStaleMs: 1, predicate: r => r.surfaceId === "openai-server" });
  assert.deepEqual(recovered.map(r => r.requestId).sort(), [dead.requestId, reusedPid.requestId]);
});

test("concurrent pollers cannot claim the same queued action", async () => {
  const root = work("concurrent");
  const queue = routeQueuePath(root, "modal-actions.jsonl", { env: env(ROUTE_A) });
  await appendRouteRecord(queue, { requestId: "concur01", surfaceId: "openai-server", action: "status" }, { env: env(ROUTE_A) });
  const results = await Promise.all([
    claimRouteRecords(queue, { env: env(ROUTE_A), predicate: r => r.surfaceId === "openai-server" }),
    claimRouteRecords(queue, { env: env(ROUTE_A), predicate: r => r.surfaceId === "openai-server" })
  ]);
  assert.equal(results.flat().filter(r => r.requestId === "concur01").length, 1);
});

test("structured audit proves active ownership without leaking routes and state stays isolated", async () => {
  const root = work("audit");
  const audit = join(root, "audit");
  const queue = routeQueuePath(root, "modal-actions.jsonl", { env: env(ROUTE_A) });
  const request = await appendRouteRecord(queue, { requestId: "audited01", surfaceId: "openai-server", action: "status" }, { env: env(ROUTE_A, audit, "active") });
  assert.deepEqual(await claimRouteRecords(queue, { env: env(ROUTE_B, audit, "passive"), predicate: r => r.surfaceId === "openai-server" }), []);
  const claimed = await claimRouteRecords(queue, { env: env(ROUTE_A, audit, "active"), predicate: r => r.surfaceId === "openai-server" });
  await writeRouteAck(join(root, "acks"), claimed[0], { ok: true }, { env: env(ROUTE_A, audit, "active") });
  await writeRouteState(join(root, "bridge-state.json"), { active: true, endpoint: "127.0.0.1:1" }, { env: env(ROUTE_A, audit, "active") });
  assert.equal((await readRouteState(join(root, "bridge-state.json"), { env: env(ROUTE_B, audit, "passive"), fallback: { endpoint: null } })).endpoint, null);
  assert.equal((await readRouteState(join(root, "bridge-state.json"), { env: env(ROUTE_A, audit, "active") })).endpoint, "127.0.0.1:1");
  const redacted = await writeRouteState(
    join(root, "bridge-state.json"),
    { active: true, routeId: ROUTE_A },
    { env: env(ROUTE_A, audit, "active") }
  );
  assert.equal("routeId" in redacted, false);
  assert.doesNotMatch(
    await readFile(join(root, "routes", routeDirectoryName(ROUTE_A), "bridge-state.json"), "utf8"),
    new RegExp(ROUTE_A)
  );

  const entries = await readAudit(audit);
  const matching = entries.filter(entry => entry.requestId === request.requestId);
  assert.deepEqual(new Set(matching.map(entry => entry.ownerLabel)), new Set(["active"]));
  assert.deepEqual(matching.map(entry => entry.operation).sort(), ["ack-write", "claim-new", "queue-append"]);
  assert.equal(entries.some(entry => entry.ownerLabel === "passive" && entry.requestId === request.requestId), false);
  const serialized = JSON.stringify(entries);
  assert.doesNotMatch(serialized, new RegExp(ROUTE_A));
  assert.doesNotMatch(serialized, new RegExp(ROUTE_B));
  assert.ok(entries.every(entry => /^[A-Za-z0-9_-]{16}$/.test(entry.routeHash)));
  assert.notEqual(createHash("sha256").update(ROUTE_A).digest("base64url").slice(0, 16), createHash("sha256").update(ROUTE_B).digest("base64url").slice(0, 16));
  assert.ok(currentClaimantIdentity().ownerId.includes(String(process.pid)));
});
