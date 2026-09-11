import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { hashRuntimePackageTree, verifyRuntimePackageIdentity } from "../../src/runtime/extension-identity.mjs";

test("runtime package hashing matches the native tree contract", async () => {
  const root = await mkdtemp(join(tmpdir(), "afterburner-runtime-identity-"));
  try {
    await mkdir(join(root, "nested"));
    await mkdir(join(root, "node_modules"));
    await writeFile(join(root, "afterburner.json"), "{\"id\":\"sample\"}\n");
    await writeFile(join(root, "nested", "extension.mjs"), "export const value = 1;\n");
    await writeFile(join(root, "node_modules", "ignored.js"), "ignored\n");

    const hash = createHash("sha256");
    for (const [name, data] of [
      ["afterburner.json", "{\"id\":\"sample\"}\n"],
      ["nested/extension.mjs", "export const value = 1;\n"],
      ["node_modules/ignored.js", "ignored\n"]
    ]) {
      hash.update(name);
      hash.update("\0");
      hash.update(data);
      hash.update("\0");
    }
    const expectedTreeHash = `sha256:${hash.digest("hex")}`;
    assert.equal(await hashRuntimePackageTree(root), expectedTreeHash);

    const manifestData = "{\"id\":\"sample\"}\n";
    const manifestHash = `sha256:${createHash("sha256").update(manifestData).digest("hex")}`;
    assert.deepEqual(await verifyRuntimePackageIdentity(root, manifestData, {
      manifestHash,
      treeHash: expectedTreeHash
    }), { manifestHash, treeHash: expectedTreeHash });
    await assert.rejects(
      () => verifyRuntimePackageIdentity(root, manifestData, null),
      error => error?.code === "extension.identityUnverified"
    );

    await writeFile(join(root, "nested", "extension.mjs"), "export const value = 2;\n");
    await assert.rejects(() => verifyRuntimePackageIdentity(root, manifestData, {
      manifestHash,
      treeHash: expectedTreeHash
    }), error => error?.code === "extension.identityChanged");
    await assert.rejects(() => verifyRuntimePackageIdentity(root, manifestData, {
      manifestHash,
      treeHash: expectedTreeHash,
      trustedBuiltin: true,
      sourceType: "signed-release"
    }), error => error?.code === "extension.identityChanged");

    await assert.rejects(() => verifyRuntimePackageIdentity(root, manifestData, {
      manifestHash,
      treeHash: expectedTreeHash,
      trustedBuiltin: false,
      sourceType: "path"
    }), error => error?.code === "extension.identityChanged");

    await assert.rejects(() => verifyRuntimePackageIdentity(root, manifestData, {
      manifestHash,
      treeHash: "sha256:tampered"
    }), error => error?.code === "extension.identityChanged");
    await assert.rejects(() => verifyRuntimePackageIdentity(root, "{\"id\":\"changed\"}\n", {
      manifestHash,
      treeHash: expectedTreeHash,
      trustedBuiltin: true,
      sourceType: "embedded"
    }), error => error?.code === "extension.identityChanged");
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});
