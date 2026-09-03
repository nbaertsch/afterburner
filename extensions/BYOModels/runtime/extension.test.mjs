import assert from "node:assert/strict";
import { mkdtemp, writeFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { activate } from "../runtime/extension.mjs";

// activate() reads its config path from AFTERBURNER_BYOMODELS_CONFIG and its
// runtime model metadata cache from COPILOT_HOME; both are pointed at an
// isolated temp directory per test so these tests never touch a real user's
// Afterburner/Copilot home.
async function withTempConfig(config, fn) {
    const dir = await mkdtemp(join(tmpdir(), "byomodels-test-"));
    const configPath = join(dir, "byomodels.json");
    await writeFile(configPath, JSON.stringify(config), "utf8");
    const previousConfig = process.env.AFTERBURNER_BYOMODELS_CONFIG;
    const previousCopilotHome = process.env.COPILOT_HOME;
    process.env.AFTERBURNER_BYOMODELS_CONFIG = configPath;
    process.env.COPILOT_HOME = dir;
    try {
        await fn(dir);
    } finally {
        if (previousConfig === undefined) delete process.env.AFTERBURNER_BYOMODELS_CONFIG;
        else process.env.AFTERBURNER_BYOMODELS_CONFIG = previousConfig;
        if (previousCopilotHome === undefined) delete process.env.COPILOT_HOME;
        else process.env.COPILOT_HOME = previousCopilotHome;
        await rm(dir, { recursive: true, force: true });
    }
}

test("activate registers a model picker row decorator that resolves each configured model's badge by selectionId", async () => {
    await withTempConfig(
        {
            version: 1,
            contextWindowOptions: [256000],
            models: [
                { provider: "my-provider", id: "my-model", modelId: "gpt-5.6-sol", badge: " 🔧 BYO" },
                { provider: "other-provider", id: "other-model", modelId: "gpt-5.5" }
            ]
        },
        async () => {
            let registeredDecorator;
            await activate({
                registerModelPickerAdapter: () => {},
                registerAppSourceTransform: () => {},
                registerModelPickerRowDecorator: (decorator) => { registeredDecorator = decorator; }
            });
            assert.ok(registeredDecorator, "expected a row decorator to be registered");
            assert.equal(registeredDecorator.id, "afterburner-byomodels-badge");
            assert.equal(registeredDecorator.render({ selectionId: "my-provider/my-model" }), " 🔧 BYO");
            assert.equal(registeredDecorator.render({ selectionId: "other-provider/other-model" }), null);
            assert.equal(registeredDecorator.render({ selectionId: "unknown/unknown" }), null);
        }
    );
});

test("activate does not register a row decorator when no configured model defines a badge", async () => {
    await withTempConfig(
        {
            version: 1,
            contextWindowOptions: [256000],
            models: [{ provider: "my-provider", id: "my-model", modelId: "gpt-5.6-sol" }]
        },
        async () => {
            let registeredDecorator;
            await activate({
                registerModelPickerAdapter: () => {},
                registerAppSourceTransform: () => {},
                registerModelPickerRowDecorator: (decorator) => { registeredDecorator = decorator; }
            });
            assert.equal(registeredDecorator, undefined);
        }
    );
});

test("activate tolerates a runtime host that does not offer registerModelPickerRowDecorator", async () => {
    await withTempConfig(
        {
            version: 1,
            contextWindowOptions: [256000],
            models: [{ provider: "my-provider", id: "my-model", modelId: "gpt-5.6-sol", badge: " 🔧 BYO" }]
        },
        async () => {
            // Older/unprofiled Afterburner runtimes may not pass this
            // function at all; activate() must not throw.
            await assert.doesNotReject(activate({
                registerModelPickerAdapter: () => {},
                registerAppSourceTransform: () => {}
            }));
        }
    );
});
