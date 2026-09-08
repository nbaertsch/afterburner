import { createHash } from "node:crypto";
import { readFile, readdir } from "node:fs/promises";
import { join, relative } from "node:path";

export async function hashRuntimePackageTree(root) {
    const hash = createHash("sha256");
    const walk = async directory => {
        const entries = await readdir(directory, { withFileTypes: true });
        entries.sort((left, right) => Buffer.compare(Buffer.from(left.name, "utf8"), Buffer.from(right.name, "utf8")));
        for (const entry of entries) {
            if (entry.isSymbolicLink()) throw new Error(`Extension package contains symbolic link '${entry.name}'.`);
            if (entry.isDirectory() && entry.name === ".git") continue;
            const path = join(directory, entry.name);
            if (entry.isDirectory()) {
                await walk(path);
                continue;
            }
            hash.update(relative(root, path).replaceAll("\\", "/"));
            hash.update("\0");
            hash.update(await readFile(path));
            hash.update("\0");
        }
    };
    await walk(root);
    return `sha256:${hash.digest("hex")}`;
}

export async function verifyRuntimePackageIdentity(root, manifestData, assertion) {
    if (!assertion || typeof assertion.manifestHash !== "string" || typeof assertion.treeHash !== "string") {
        const error = new Error(`Afterburner extension package has no verified native identity: ${root}`);
        error.code = "extension.identityUnverified";
        throw error;
    }
    const manifestHash = `sha256:${createHash("sha256").update(manifestData).digest("hex")}`;
    const treeHash = await hashRuntimePackageTree(root);
    if (manifestHash !== assertion.manifestHash || treeHash !== assertion.treeHash) {
        const error = new Error(`Afterburner extension package changed after native verification: ${root}`);
        error.code = "extension.identityChanged";
        throw error;
    }
    return { manifestHash, treeHash };
}
