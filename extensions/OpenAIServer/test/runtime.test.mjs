import assert from "node:assert/strict";
import { randomBytes } from "node:crypto";
import { rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { requestModalOpen } from "../extensions/OpenAIServer/modal-ipc.mjs";
import { activate, MODAL_ACTIVATION_FALLBACK_MS } from "../runtime/extension.mjs";

const ROUTE = "routeRuntimeWatcherAAAAAAAAAAAAAAAAAAAAAAAA";

test("runtime watches modal activation with low-frequency fallback", async t => {
    const previousHome = process.env.AFTERBURNER_HOME;
    const previousRoute = process.env.AFTERBURNER_SESSION_ROUTE;
    const home = join(tmpdir(), `afterburner-openai-runtime-${process.pid}-${randomBytes(4).toString("hex")}`);
    process.env.AFTERBURNER_HOME = home;
    process.env.AFTERBURNER_SESSION_ROUTE = ROUTE;
    t.after(async () => {
        if (previousHome === undefined) delete process.env.AFTERBURNER_HOME;
        else process.env.AFTERBURNER_HOME = previousHome;
        if (previousRoute === undefined) delete process.env.AFTERBURNER_SESSION_ROUTE;
        else process.env.AFTERBURNER_SESSION_ROUTE = previousRoute;
        await rm(home, { recursive: true, force: true });
    });

    const intervals = [];
    const originalSetInterval = globalThis.setInterval;
    globalThis.setInterval = (handler, delay, ...args) => {
        intervals.push(delay);
        return originalSetInterval(handler, delay, ...args);
    };
    t.after(() => { globalThis.setInterval = originalSetInterval; });

    let opens = 0;
    const instance = await activate({
        registerModalCanvas: definition => ({
            open: async input => {
                opens++;
                return definition.open(input);
            },
            close: async () => ({ ok: true }),
            dispose() {}
        })
    });
    t.after(() => instance?.dispose());

    assert.ok(MODAL_ACTIVATION_FALLBACK_MS >= 1000);
    assert.ok(intervals.includes(MODAL_ACTIVATION_FALLBACK_MS));
    const result = await requestModalOpen({ timeoutMs: 1500 });
    assert.equal(result.ok, true);
    assert.equal(opens, 1);
});
