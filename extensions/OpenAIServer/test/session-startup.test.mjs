import assert from "node:assert/strict";
import { setTimeout as delay } from "node:timers/promises";
import { test } from "node:test";
import { registerThenInitialize } from "../extensions/OpenAIServer/session-startup.mjs";

test("OpenAI commands register while optional initialization is blocked", async () => {
    let releaseInitialization;
    const blocked = new Promise(resolve => { releaseInitialization = resolve; });
    let registered;
    const logs = [];
    const startup = await Promise.race([
        registerThenInitialize({
            joinSession: async registration => {
                registered = registration;
                return { log: async message => logs.push(message) };
            },
            registration: { commands: [{ name: "openai-server" }], canvases: [] },
            initialize: () => blocked
        }),
        delay(250).then(() => assert.fail("OpenAI activation waited for optional initialization"))
    ]);

    assert.equal(registered.commands[0].name, "openai-server");
    releaseInitialization();
    await startup.ready;
    assert.deepEqual(logs, []);
});

test("deferred OpenAI startup errors are visible unless disposed", async () => {
    let rejectInitialization;
    const blocked = new Promise((_resolve, reject) => { rejectInitialization = reject; });
    const logs = [];
    const startup = await registerThenInitialize({
        joinSession: async () => ({ log: async message => logs.push(message) }),
        registration: { commands: [], canvases: [] },
        initialize: () => blocked
    });
    rejectInitialization(new Error("session readiness unavailable"));
    await startup.ready;
    assert.match(logs[0], /OpenAI Server startup failed: session readiness unavailable/);

    let rejectAfterDispose;
    const disposedLogs = [];
    const disposed = await registerThenInitialize({
        joinSession: async () => ({ log: async message => disposedLogs.push(message) }),
        registration: { commands: [], canvases: [] },
        initialize: () => new Promise((_resolve, reject) => { rejectAfterDispose = reject; })
    });
    disposed.dispose();
    rejectAfterDispose(new Error("late failure"));
    await disposed.ready;
    assert.deepEqual(disposedLogs, []);
});
