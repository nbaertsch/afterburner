import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";
import test from "node:test";
import vm from "node:vm";

const testDirectory = dirname(fileURLToPath(import.meta.url));
const appJsPath = join(testDirectory, "..", "internal", "runtimepkg", "app.js");

// Fixture bundle text for each known compatibility profile's model picker row
// renderer and row-identity map builder, copied verbatim from the real
// anchors BYOModels' context-arrow transform already patches
// (extensions/BYOModels/runtime/extension.mjs) and from the real installed
// Copilot bundles (verified directly against
// %LOCALAPPDATA%/.afterburner/copilot-home/pkg/win32-x64/<version>/app.js
// for all three profiles). These are the same anchors
// internal/compatibility's ModelPickerRowRenderer profile field and
// internal/runtimepkg/app.js's modelPickerRowRendererByProfile point at.
const rendererFixtures = {
    "copilot-1.0.83-1-win32-x64": {
        rendererName: "vzr",
        rowIdentityMapAnchor: "j=(0,Kn.useMemo)(()=>{let Ue=new Map;for(let nt of r){let ut=ne.get(nt.value);ut&&Ue.set(ut.rowKey,nt)}return Ue},[r,ne])",
        source: 'function vzr(e,t,n){return t?Kn.default.createElement(Kn.default.Fragment,null,e.contextSegments.map(r=>Kn.default.createElement(Kn.default.Fragment,{key:r.key},Kn.default.createElement(b,{color:r.active?n.primary:n.muted},r.text)))):Kn.default.createElement(b,{color:n.muted},e.contextCellText)}',
        identitySource: "j=(0,Kn.useMemo)(()=>{let Ue=new Map;for(let nt of r){let ut=ne.get(nt.value);ut&&Ue.set(ut.rowKey,nt)}return Ue},[r,ne])"
    },
    "copilot-1.0.83-2-win32-x64": {
        rendererName: "Wzr",
        rowIdentityMapAnchor: "j=(0,Vn.useMemo)(()=>{let Ue=new Map;for(let nt of r){let ut=ne.get(nt.value);ut&&Ue.set(ut.rowKey,nt)}return Ue},[r,ne])",
        source: 'function Wzr(e,t,n){return t?Vn.default.createElement(Vn.default.Fragment,null,e.contextSegments.map(r=>Vn.default.createElement(Vn.default.Fragment,{key:r.key},Vn.default.createElement(b,{color:r.active?n.primary:n.muted},r.text)))):Vn.default.createElement(b,{color:n.muted},e.contextCellText)}',
        identitySource: "j=(0,Vn.useMemo)(()=>{let Ue=new Map;for(let nt of r){let ut=ne.get(nt.value);ut&&Ue.set(ut.rowKey,nt)}return Ue},[r,ne])"
    },
    "copilot-1.0.83-3-win32-x64": {
        rendererName: "U6r",
        rowIdentityMapAnchor: "V=(0,Wn.useMemo)(()=>{let Le=new Map;for(let tt of r){let Ct=re.get(tt.value);Ct&&Le.set(Ct.rowKey,tt)}return Le},[r,re])",
        source: 'function U6r(e,t,n){return t?Wn.default.createElement(Wn.default.Fragment,null,e.contextSegments.map(r=>Wn.default.createElement(Wn.default.Fragment,{key:r.key},Wn.default.createElement(b,{color:r.active?n.primary:n.muted},r.text)))):Wn.default.createElement(b,{color:n.muted},e.contextCellText)}',
        identitySource: "V=(0,Wn.useMemo)(()=>{let Le=new Map;for(let tt of r){let Ct=re.get(tt.value);Ct&&Le.set(Ct.rowKey,tt)}return Le},[r,re])"
    }
};

// buildBundle concatenates a profile's identity-map anchor and renderer
// anchor into a single fake bundle string, mirroring how both anchors
// coexist (at different offsets) inside the real, single-file Copilot
// app.js bundle.
function buildBundle(fixture) {
    return `${fixture.identitySource};${fixture.source}`;
}


// extractPurePatchLogic pulls just the standalone functions under test out of
// the real app.js source (which is a top-level executing module that cannot
// be imported directly in a test process) and evaluates them in an isolated
// VM context, exposing them for direct assertions. This keeps the test
// exercising the actual shipped source rather than a duplicated copy.
async function loadUiModFunctions() {
    const source = await readFile(appJsPath, "utf8");
    const extract = (name) => {
        const marker = `function ${name}(`;
        const start = source.indexOf(marker);
        assert.ok(start >= 0, `could not find ${name} in app.js`);
        let depth = 0;
        let index = source.indexOf("{", start);
        let inString = null;
        let inTemplate = false;
        let templateDepth = 0;
        let inLineComment = false;
        for (; index < source.length; index++) {
            const char = source[index];
            const prev = source[index - 1];
            if (inLineComment) {
                if (char === "\n") inLineComment = false;
                continue;
            }
            if (inString) {
                if (char === inString && prev !== "\\") inString = null;
                continue;
            }
            if (inTemplate) {
                if (char === "`" && prev !== "\\") inTemplate = false;
                else if (char === "{" && prev === "$") templateDepth++;
                else if (char === "}" && templateDepth > 0) templateDepth--;
                continue;
            }
            if (char === "/" && source[index + 1] === "/") { inLineComment = true; continue; }
            if (char === "\"" || char === "'") { inString = char; continue; }
            if (char === "`") { inTemplate = true; continue; }
            if (char === "{") depth++;
            else if (char === "}") {
                depth--;
                if (depth === 0) break;
            }
        }
        return source.slice(start, index + 1);
    };
    const context = {
        process: { env: { ...process.env } },
        console
    };
    vm.createContext(context);
    const harness = `
        ${extract("currentCompatibilityProfileID")}
        ${extract("applyModelPickerRowDecorators")}
        globalThis.applyModelPickerRowDecorators = applyModelPickerRowDecorators;
        globalThis.__test_setProfile = (id) => { process.env.AFTERBURNER_COMPATIBILITY_PROFILE = id; };
    `;
    const registryDeclaration = `
        const uiModRegistry = { modelPickerRowDecorators: [] };
        const modelPickerRowRendererByProfile = ${JSON.stringify(
            Object.fromEntries(Object.entries(rendererFixtures).map(([id, v]) => [
                id,
                { rendererName: v.rendererName, rowIdentityMapAnchor: v.rowIdentityMapAnchor }
            ]))
        )};
        globalThis.__test_registry = uiModRegistry;
        function reportInstrumentationFailure(seam, error) {
            globalThis.__test_failures = globalThis.__test_failures ?? [];
            globalThis.__test_failures.push({ seam, message: error?.message });
        }
    `;
    vm.runInContext(registryDeclaration + harness, context);
    return context;
}

test("model picker row decorator patches the exact profile-specific renderer once, and resolves the real selectionId via the row identity map", async () => {
    for (const [profileID, fixture] of Object.entries(rendererFixtures)) {
        const context = await loadUiModFunctions();
        context.__test_setProfile(profileID);
        let observedSelectionId;
        context.__test_registry.modelPickerRowDecorators.push({
            id: "test-badge",
            render: (row) => {
                observedSelectionId = row.selectionId;
                return " 🔧 BYO";
            }
        });
        const bundle = buildBundle(fixture);
        const patched = context.applyModelPickerRowDecorators(bundle);
        assert.notEqual(patched, bundle, `expected a patch for ${profileID}`);
        assert.match(patched, new RegExp(`globalThis\\.__afterburnerModelPickerRowDecorators`));
        // Exercise the installed identity map + bridge end-to-end: simulate
        // Copilot's identity-map builder loop registering a row, then Copilot
        // calling the patched renderer with that row's rendered object, and
        // confirm the decorator recovers the true selectionId (not just the
        // opaque rowKey) and the emoji badge text round-trips intact.
        const identityMap = context.__afterburnerModelPickerRowIdentity;
        // Note: the VM sandbox is a separate realm, so its Map constructor is
        // not identical to this process's Map -- `instanceof Map` would
        // always be false here even for a real Map. Duck-type instead.
        assert.equal(typeof identityMap?.set, "function", "expected a Map-like identity bridge");
        identityMap.set("row-key-abc", "my-provider/my-model");
        const rowObject = { rowKey: "row-key-abc", contextCellText: "200K", contextSegments: [] };
        const bridgeFn = context.__afterburnerModelPickerRowDecorators;
        assert.equal(typeof bridgeFn, "function");
        const badge = bridgeFn(rowObject);
        assert.equal(badge, " 🔧 BYO");
        assert.equal(observedSelectionId, "my-provider/my-model");
    }
});

test("model picker row decorator is a no-op with zero registered decorators", async () => {
    const context = await loadUiModFunctions();
    context.__test_setProfile("copilot-1.0.83-3-win32-x64");
    const bundle = buildBundle(rendererFixtures["copilot-1.0.83-3-win32-x64"]);
    const patched = context.applyModelPickerRowDecorators(bundle);
    assert.equal(patched, bundle);
});

test("model picker row decorator fails closed for an unknown compatibility profile", async () => {
    const context = await loadUiModFunctions();
    context.__test_setProfile("copilot-9.9.9-unknown-win32-x64");
    context.__test_registry.modelPickerRowDecorators.push({ id: "test-badge", render: () => " X" });
    const bundle = buildBundle(rendererFixtures["copilot-1.0.83-3-win32-x64"]);
    const patched = context.applyModelPickerRowDecorators(bundle);
    assert.equal(patched, bundle, "unknown profile must never be patched");
});

test("model picker row decorator fails closed when the renderer anchor does not match exactly once", async () => {
    const context = await loadUiModFunctions();
    context.__test_setProfile("copilot-1.0.83-3-win32-x64");
    context.__test_registry.modelPickerRowDecorators.push({ id: "test-badge", render: () => " X" });
    const fixture = rendererFixtures["copilot-1.0.83-3-win32-x64"];
    const duplicated = `${fixture.identitySource};${fixture.source};${fixture.source}`;
    const patched = context.applyModelPickerRowDecorators(duplicated);
    assert.equal(patched, duplicated, "duplicated renderer anchor must never be patched");
});

test("model picker row decorator fails closed when the identity map anchor does not match exactly once", async () => {
    const context = await loadUiModFunctions();
    context.__test_setProfile("copilot-1.0.83-3-win32-x64");
    context.__test_registry.modelPickerRowDecorators.push({ id: "test-badge", render: () => " X" });
    const fixture = rendererFixtures["copilot-1.0.83-3-win32-x64"];
    const duplicated = `${fixture.identitySource};${fixture.identitySource};${fixture.source}`;
    const patched = context.applyModelPickerRowDecorators(duplicated);
    assert.equal(patched, duplicated, "duplicated identity map anchor must never be patched");
});

test("model picker row decorator isolates a throwing decorator from others", async () => {
    const context = await loadUiModFunctions();
    context.__test_setProfile("copilot-1.0.83-3-win32-x64");
    context.__test_registry.modelPickerRowDecorators.push(
        { id: "throws", render: () => { throw new Error("boom"); } },
        { id: "ok", render: () => " OK" }
    );
    const bundle = buildBundle(rendererFixtures["copilot-1.0.83-3-win32-x64"]);
    context.applyModelPickerRowDecorators(bundle);
    const identityMap = context.__afterburnerModelPickerRowIdentity;
    identityMap.set("row-key-xyz", "some/model");
    const bridgeFn = context.__afterburnerModelPickerRowDecorators;
    const badge = bridgeFn({ rowKey: "row-key-xyz", contextCellText: "200K", contextSegments: [] });
    assert.equal(badge, " OK");
    assert.equal(context.__test_failures?.[0]?.seam, "modelPickerRowDecorator:throws");
});
