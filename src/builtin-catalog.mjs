import { existsSync, readFileSync, readdirSync, statSync } from "node:fs";
import { join } from "node:path";

const legacyIds = new Map([
    ["byomodels", "byo-models"]
]);

export function canonicalExtensionId(id) {
    return legacyIds.get(id) ?? id;
}

export function discoverBuiltins(extensionsRoot) {
    const catalog = new Map();
    if (!existsSync(extensionsRoot)) return catalog;

    for (const name of readdirSync(extensionsRoot).sort()) {
        const packagePath = join(extensionsRoot, name);
        if (!statSync(packagePath).isDirectory()) continue;
        const manifestPath = join(packagePath, "afterburner.json");
        if (!existsSync(manifestPath)) continue;

        const packageManifest = JSON.parse(readFileSync(manifestPath, "utf8").replace(/^\uFEFF/, ""));
        if (packageManifest.visibility !== "builtin") continue;
        const id = canonicalExtensionId(packageManifest.id);
        if (!/^[a-z0-9][a-z0-9-]{0,63}$/.test(id))
            throw new Error(`Invalid built-in extension ID in '${manifestPath}'.`);
        if (catalog.has(id))
            throw new Error(`Duplicate built-in extension ID '${id}'.`);
        catalog.set(id, { id, path: packagePath, manifest: { ...packageManifest, id } });
    }
    return catalog;
}
