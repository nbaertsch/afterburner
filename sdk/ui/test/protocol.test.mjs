import assert from "node:assert/strict";
import { PassThrough } from "node:stream";
import test from "node:test";
import {
  MemoryTransport,
  FrameTransport,
  ProtocolClient,
  application,
  createEnvelope,
  createHello,
  createTestHost,
  createUIDocument,
  encodeFrame,
  decodeFrame,
  negotiateHello,
  text
} from "../dist/afterburner-ui.mjs";

function doc(revision = 1) {
  return createUIDocument(application({ title: "Protocol" }, [text("ready")], { id: "app-root" }), { surfaceId: "surface-main", revision });
}

test("handshake, component snapshot and acks use afterburner.ui.r1 sequence/epoch", async () => {
  const host = createTestHost({ epoch: 3, generation: 5 });
  const client = new ProtocolClient({ transport: host.clientTransport, extensionId: "black-box", epoch: 3, generation: 5 });
  await client.connect();
  const result = await client.snapshot(doc());
  assert.equal(result.ok, true);
  assert.equal(host.received[0].kind, "hello");
  assert.equal(host.received[0].payload.compatibility[0], "afterburner.ui.r1");
  const snapshot = host.received.find((message) => message.kind === "component.snapshot");
  assert.equal(snapshot.epoch, 3);
  assert.equal(snapshot.generation, 5);
  assert.equal(snapshot.sequence, 1);
});

test("framing encoder/decoder enforces frame size and JSON depth", async () => {
  const frame = encodeFrame(createEnvelope("ack", {}, { sequence: 1, ack: { epoch: 1, highWater: 1, contiguous: true } }));
  const decoded = decodeFrame(frame);
  assert.equal(decoded.kind, "ack");
  assert.throws(() => encodeFrame({ x: "12345" }, { maxFrameBytes: 4 }), /frame exceeds/);
  assert.throws(() => encodeFrame([[[[[1]]]]], { maxJsonDepth: 3 }), /depth/);

  const wire = new PassThrough();
  const transport = new FrameTransport(wire);
  const seen = new Promise((resolve) => transport.once("message", resolve));
  transport.write(createEnvelope("ack", {}, { sequence: 2, ack: { epoch: 1, highWater: 2, contiguous: true } }));
  assert.equal((await seen).sequence, 2);
});

test("negotiation rejects stale/downgrade and client ignores stale epoch acks", async () => {
  const local = createHello({ epoch: 2 });
  const remote = createHello({ epoch: 1 });
  remote.supportedRevisions = [1];
  remote.preferredRevision = 1;
  assert.equal(negotiateHello(local, remote).accepted, true);
  local.supportedRevisions = [2];
  local.preferredRevision = 2;
  local.rejectDowngrade = true;
  assert.equal(negotiateHello(local, remote).accepted, false);

  const [clientTransport, hostTransport] = MemoryTransport.pair();
  hostTransport.on("message", (envelope) => {
    if (envelope.kind === "hello") {
      hostTransport.write(createEnvelope("hello.result", { accepted: true }, { epoch: 10, sequence: 1 }));
    }
  });
  const client = new ProtocolClient({ transport: clientTransport, epoch: 10 });
  await client.connect();
  const pending = client.snapshot(doc());
  hostTransport.write(createEnvelope("ack", {}, { epoch: 9, sequence: 98, ack: { epoch: 9, highWater: 1, contiguous: true } }));
  let settled = false;
  pending.then(() => { settled = true; });
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(settled, false);
  hostTransport.write(createEnvelope("ack", {}, { epoch: 10, sequence: 99, ack: { epoch: 10, highWater: 1, contiguous: true } }));
  await pending;
});

test("backpressure queues until release and reconnect advances epoch", async () => {
  const host = createTestHost({ epoch: 1, backpressure: true });
  const client = new ProtocolClient({ transport: host.clientTransport, epoch: 1, maxQueue: 2 });
  await client.connect();
  await client.snapshot(doc());
  const queued = client.snapshot(doc(2));
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(host.received.filter((message) => message.kind === "component.snapshot").length, 1);
  host.releaseBackpressure();
  await queued;
  assert.equal(host.received.filter((message) => message.kind === "component.snapshot").length, 2);

  const nextHost = createTestHost({ epoch: 2 });
  await client.reconnect({ transport: nextHost.clientTransport });
  await client.snapshot(doc(3));
  assert.equal(client.epoch, 2);
  assert.equal(nextHost.received.find((message) => message.kind === "component.snapshot").epoch, 2);
});

test("transport crash emits disconnect and reconnect reconciles with snapshot", async () => {
  const host = createTestHost({ epoch: 4 });
  const client = new ProtocolClient({ transport: host.clientTransport, epoch: 4 });
  let disconnected = false;
  client.on("disconnected", () => { disconnected = true; });
  await client.connect();
  host.crash();
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(disconnected, true);
  const nextHost = createTestHost({ epoch: 5 });
  await client.reconnect({ transport: nextHost.clientTransport });
  await client.snapshot(doc(4));
  assert.equal(nextHost.received.at(-1).kind, "component.snapshot");
  assert.equal(nextHost.received.at(-1).epoch, 5);
});

test("AbortSignal cancellation rejects pending sends", async () => {
  const host = createTestHost({ epoch: 1 });
  const client = new ProtocolClient({ transport: host.clientTransport, epoch: 1 });
  await client.connect();
  host.hostTransport.removeAllListeners("message");
  const controller = new AbortController();
  const promise = client.snapshot(doc(), { signal: controller.signal });
  controller.abort();
  await assert.rejects(promise, /operation canceled/);
});
