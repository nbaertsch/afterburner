import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import { cpSync, existsSync, mkdirSync, readFileSync, readdirSync, renameSync, rmSync, statSync, writeFileSync } from "node:fs";
import { basename, dirname, join, relative, resolve, sep } from "node:path";
import { canonicalExtensionId, discoverBuiltins } from "./builtin-catalog.mjs";

const root = process.env.AFTERBURNER_HOME ?? join(process.env.USERPROFILE ?? "", ".afterburner");
const installedRoot = join(root, "extensions");
const registryPath = join(root, "registry.json");

function readJson(path) {
    return JSON.parse(readFileSync(path, "utf8").replace(/^\uFEFF/, ""));
}

function saveJson(path, value) {
    mkdirSync(dirname(path), { recursive: true });
    const temporary = `${path}.${process.pid}.tmp`;
    writeFileSync(temporary, `${JSON.stringify(value, null, 2)}\n`, "utf8");
    renameSync(temporary, path);
}

function save(value) {
    saveJson(registryPath, value);
}

function isWithin(path, parent) {
    if (typeof path !== "string") return false;
    const child = resolve(path).toLowerCase();
    const rootPath = resolve(parent).toLowerCase();
    return child === rootPath || child.startsWith(`${rootPath}${sep}`);
}

function rewriteLegacyPackageManifest(path) {
    const manifestPath = join(path, "afterburner.json");
    if (!existsSync(manifestPath)) return;
    const value = readJson(manifestPath);
    const id = canonicalExtensionId(value.id);
    if (id !== value.id) saveJson(manifestPath, { ...value, id });
}

function mergeLegacyPackageCache() {
    const legacyRoot = join(installedRoot, "byomodels");
    const canonicalRoot = join(installedRoot, "byo-models");
    if (!existsSync(legacyRoot)) return false;
    if (!existsSync(canonicalRoot)) {
        mkdirSync(dirname(canonicalRoot), { recursive: true });
        renameSync(legacyRoot, canonicalRoot);
    } else {
        for (const name of readdirSync(legacyRoot)) {
            const source = join(legacyRoot, name);
            const target = join(canonicalRoot, name);
            if (!existsSync(target)) renameSync(source, target);
        }
        rmSync(legacyRoot, { recursive: true, force: true });
    }
    for (const name of readdirSync(canonicalRoot)) {
        const versionPath = join(canonicalRoot, name);
        if (statSync(versionPath).isDirectory()) rewriteLegacyPackageManifest(versionPath);
    }
    return true;
}

function migrateLegacyRegistry(value) {
    value.extensions ??= {};
    const legacy = value.extensions.byomodels;
    const movedCache = mergeLegacyPackageCache();
    if (!legacy) return movedCache;

    const legacyRoot = join(installedRoot, "byomodels");
    const canonicalRoot = join(installedRoot, "byo-models");
    const rewritePath = path => isWithin(path, legacyRoot)
        ? join(canonicalRoot, relative(legacyRoot, path))
        : path;
    const migrated = {
        ...legacy,
        activePath: rewritePath(legacy.activePath),
        previousActivePath: rewritePath(legacy.previousActivePath),
        manifest: legacy.manifest ? { ...legacy.manifest, id: "byo-models" } : legacy.manifest
    };
    if (!value.extensions["byo-models"]) value.extensions["byo-models"] = migrated;
    delete value.extensions.byomodels;
    return true;
}

function registry() {
    let value;
    try { value = readJson(registryPath); }
    catch { value = { schemaVersion: 1, extensions: {} }; }
    if (migrateLegacyRegistry(value)) save(value);
    return value;
}

function manifest(path) {
    const value = readJson(join(path, "afterburner.json"));
    value.id = canonicalExtensionId(value.id);
    if (value.schemaVersion !== 1 || !/^[a-z0-9][a-z0-9-]{0,63}$/.test(value.id) ||
        value.runtime?.execution !== "in-process" || typeof value.runtime?.entrypoint !== "string")
        throw new Error("Invalid afterburner.json manifest.");
    for (const entrypoint of [value.runtime.entrypoint, value.sessionExtension?.entrypoint].filter(Boolean)) {
        if (!existsSync(join(path, entrypoint))) throw new Error(`Entrypoint does not exist: ${entrypoint}`);
    }
    return value;
}

function localVersion(path) {
    const hash = createHash("sha256");
    const visit = (current, relativePath = "") => {
        for (const name of readdirSync(current).sort()) {
            if (name === ".git" || name === "node_modules" || name === "bin" || name === "obj") continue;
            const absolute = join(current, name);
            const child = join(relativePath, name);
            if (statSync(absolute).isDirectory()) visit(absolute, child);
            else {
                hash.update(child);
                hash.update(readFileSync(absolute));
            }
        }
    };
    visit(path);
    return `local-${hash.digest("hex").slice(0, 16)}`;
}

function sourcePath(spec) {
    if (!spec) throw new Error("An extension source is required.");
    const local = resolve(spec);
    if (existsSync(join(local, "afterburner.json"))) {
        return { path: local, temporary: false, source: { type: "path", value: local, version: localVersion(local) } };
    }
    const temporary = join(root, "staging", `${Date.now()}-${basename(spec).replace(/[^A-Za-z0-9.-]/g, "_")}`);
    mkdirSync(dirname(temporary), { recursive: true });
    const shorthand = /^[\w.-]+\/[\w.-]+(?:@.+)?$/.test(spec);
    const url = shorthand ? `git@github.com:${spec.split("@")[0]}.git` : spec;
    const ref = shorthand ? /^[\w.-]+\/[\w.-]+@(.+)$/.exec(spec)?.[1] : undefined;
    execFileSync("git", ["clone", "--quiet", ...(ref ? ["--branch", ref] : []), url, temporary], { stdio: "inherit" });
    const commit = execFileSync("git", ["-C", temporary, "rev-parse", "HEAD"], { encoding: "utf8" }).trim();
    return { path: temporary, temporary: true, source: { type: "git", value: url, ref: ref ?? null, commit } };
}

function sourceSpec(source) {
    if (source.type === "path") return source.value;
    const base = source.value;
    return source.ref ? `${base}@${source.ref}` : base;
}

function installResolved(value, resolved, { preserve, expectedId, enabled } = {}) {
    try {
        const packageManifest = manifest(resolved.path);
        const canonicalExpectedId = expectedId ? canonicalExtensionId(expectedId) : undefined;
        if (canonicalExpectedId && packageManifest.id !== canonicalExpectedId)
            throw new Error(`Source package changed identity from '${canonicalExpectedId}' to '${packageManifest.id}'.`);
        const version = resolved.source.commit ?? resolved.source.version;
        const target = join(installedRoot, packageManifest.id, version);
        if (!existsSync(target)) {
            const staging = `${target}.staging-${process.pid}`;
            rmSync(staging, { recursive: true, force: true });
            mkdirSync(dirname(target), { recursive: true });
            cpSync(resolved.path, staging, { recursive: true, filter: source => basename(source) !== ".git" });
            saveJson(join(staging, "afterburner.json"), packageManifest);
            renameSync(staging, target);
        } else {
            rewriteLegacyPackageManifest(target);
        }
        const previous = value.extensions[packageManifest.id];
        value.extensions[packageManifest.id] = {
            enabled: enabled ?? (preserve ? previous?.enabled ?? false : false),
            activePath: target,
            previousActivePath: preserve && previous?.activePath !== target
                ? previous?.activePath ?? null
                : previous?.previousActivePath ?? null,
            manifest: packageManifest,
            source: resolved.source,
            updatedAt: new Date().toISOString()
        };
        save(value);
        return { id: packageManifest.id, target, changed: previous?.activePath !== target };
    } finally {
        if (resolved.temporary) rmSync(resolved.path, { recursive: true, force: true });
    }
}

function updateOne(value, rawId) {
    const id = canonicalExtensionId(rawId);
    const current = value.extensions[id];
    if (!current) throw new Error(`Unknown Afterburner extension '${id}'.`);
    const result = installResolved(value, sourcePath(sourceSpec(current.source)), { preserve: true, expectedId: id });
    console.log(result.changed ? `Updated '${id}' to ${basename(result.target)}.` : `'${id}' is already current.`);
}

function selectBuiltins(value, extensionsRoot, ids, { allowInstalled = false } = {}) {
    const catalog = discoverBuiltins(extensionsRoot);
    const selectedIds = ids.length > 0 ? ids.map(canonicalExtensionId) : [...catalog.keys()];
    if (new Set(selectedIds).size !== selectedIds.length) throw new Error("Duplicate built-in extension ID.");
    for (const id of selectedIds) {
        if (!catalog.has(id) && !(allowInstalled && value.extensions[id]?.manifest?.visibility === "builtin")) {
            const available = [...catalog.keys()].join(", ") || "none";
            throw new Error(`Unknown built-in extension '${id}'. Available built-ins: ${available}.`);
        }
    }
    return { catalog, ids: selectedIds };
}

function installBuiltins(value, extensionsRoot, ids) {
    const selected = selectBuiltins(value, extensionsRoot, ids);
    for (const id of selected.ids) manifest(selected.catalog.get(id).path);
    for (const id of selected.ids) {
        const item = selected.catalog.get(id);
        const result = installResolved(value, sourcePath(item.path), { preserve: true, expectedId: id, enabled: true });
        console.log(`Installed and enabled built-in extension '${id}' at ${basename(result.target)}.`);
    }
}

function setBuiltinsEnabled(value, extensionsRoot, ids, enabled) {
    if (ids.length === 0) throw new Error(`At least one built-in extension ID is required.`);
    const selected = selectBuiltins(value, extensionsRoot, ids, { allowInstalled: true });
    for (const id of selected.ids) {
        if (!value.extensions[id]) throw new Error(`Built-in extension '${id}' is not installed. Run 'afterburn install ${id}'.`);
    }
    for (const id of selected.ids) {
        value.extensions[id].enabled = enabled;
        console.log(`${enabled ? "Enabled" : "Disabled"} built-in extension '${id}'.`);
    }
    save(value);
}

function uninstallBuiltins(value, extensionsRoot, ids) {
    if (ids.length === 0) throw new Error("At least one built-in extension ID is required.");
    const selected = selectBuiltins(value, extensionsRoot, ids, { allowInstalled: true });
    for (const id of selected.ids) {
        delete value.extensions[id];
        rmSync(join(installedRoot, id), { recursive: true, force: true });
        console.log(`Uninstalled built-in extension '${id}'.`);
    }
    save(value);
}

function reconcileSessionExtensions(value, configPath) {
    const config = existsSync(configPath) ? readJson(configPath) : {};
    const existing = Array.isArray(config.installedPlugins) ? config.installedPlugins : [];
    const desired = [];

    for (const entry of Object.values(value.extensions)) {
        if (!entry.enabled || !entry.manifest?.sessionExtension) continue;
        const pluginManifestPath = join(entry.activePath, "plugin.json");
        if (!existsSync(pluginManifestPath))
            throw new Error(`Session component for '${entry.manifest.id}' is missing plugin.json.`);
        const pluginManifest = readJson(pluginManifestPath);
        const previous = existing.find(plugin => plugin?.name === pluginManifest.name && plugin?.cache_path === entry.activePath);
        desired.push({
            name: pluginManifest.name,
            marketplace: "",
            version: pluginManifest.version,
            installed_at: previous?.installed_at ?? new Date().toISOString(),
            cache_path: entry.activePath,
            enabled: true,
            source: { source: "local", path: entry.activePath }
        });
    }

    const desiredNames = new Set(desired.map(plugin => plugin.name));
    const retained = existing.filter(plugin =>
        !desiredNames.has(plugin?.name) &&
        !isWithin(plugin?.cache_path, installedRoot) &&
        !isWithin(plugin?.source?.path, installedRoot));
    config.installedPlugins = [...retained, ...desired];
    if (existsSync(configPath) || config.installedPlugins.length > 0) saveJson(configPath, config);
}

const [command, ...arguments_] = process.argv.slice(2);
const value = registry();
if (command === "install") {
    const result = installResolved(value, sourcePath(arguments_[0]));
    console.log(`Installed Afterburner extension '${result.id}' disabled at ${basename(result.target)}.`);
    console.log("This package executes trusted JavaScript inside the Copilot process. Inspect it before enabling.");
} else if (command === "install-builtins") {
    installBuiltins(value, arguments_[0], arguments_.slice(1));
} else if (command === "enable-builtins" || command === "disable-builtins") {
    setBuiltinsEnabled(value, arguments_[0], arguments_.slice(1), command === "enable-builtins");
} else if (command === "uninstall-builtins") {
    uninstallBuiltins(value, arguments_[0], arguments_.slice(1));
} else if (command === "reconcile-session") {
    reconcileSessionExtensions(value, arguments_[0]);
} else if (command === "catalog") {
    for (const id of discoverBuiltins(arguments_[0]).keys()) console.log(id);
} else if (command === "update") {
    if (arguments_[0] === "--all") for (const id of Object.keys(value.extensions)) updateOne(value, id);
    else updateOne(value, arguments_[0]);
} else if (command === "rollback") {
    const id = canonicalExtensionId(arguments_[0]);
    const entry = value.extensions[id];
    if (!entry) throw new Error(`Unknown Afterburner extension '${id}'.`);
    if (!entry.previousActivePath || !existsSync(entry.previousActivePath))
        throw new Error(`No rollback version is available for '${id}'.`);
    [entry.activePath, entry.previousActivePath] = [entry.previousActivePath, entry.activePath];
    entry.manifest = manifest(entry.activePath);
    entry.updatedAt = new Date().toISOString();
    save(value);
    console.log(`Rolled back '${id}' to ${basename(entry.activePath)}.`);
} else if (command === "enable" || command === "disable") {
    const id = canonicalExtensionId(arguments_[0]);
    if (!value.extensions[id]) throw new Error(`Unknown Afterburner extension '${id}'.`);
    value.extensions[id].enabled = command === "enable";
    save(value);
    console.log(`${command === "enable" ? "Enabled" : "Disabled"} '${id}'.`);
} else if (command === "list") {
    for (const [id, entry] of Object.entries(value.extensions))
        console.log(`${entry.enabled ? "enabled " : "disabled"} ${id} (${entry.manifest.visibility ?? "private"}) ${basename(entry.activePath)}`);
} else if (command === "inspect") {
    const id = canonicalExtensionId(arguments_[0]);
    if (!value.extensions[id]) throw new Error(`Unknown Afterburner extension '${id}'.`);
    console.log(JSON.stringify(value.extensions[id], null, 2));
} else {
    throw new Error("Usage: extension-manager.mjs install <source> | install-builtins <extensions-root> [id...] | enable-builtins|disable-builtins|uninstall-builtins <extensions-root> <id...> | update <id>|--all | rollback <id> | enable|disable|inspect <id> | list");
}
