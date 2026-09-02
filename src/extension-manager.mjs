import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import { cpSync, existsSync, mkdirSync, readFileSync, readdirSync, renameSync, rmSync, statSync, writeFileSync } from "node:fs";
import { basename, dirname, join, resolve } from "node:path";

const root = process.env.AFTERBURNER_HOME ?? join(process.env.USERPROFILE ?? "", ".afterburner");
const installedRoot = join(root, "extensions");
const registryPath = join(root, "registry.json");

function registry() {
    try { return JSON.parse(readFileSync(registryPath, "utf8")); }
    catch { return { schemaVersion: 1, extensions: {} }; }
}

function save(value) {
    mkdirSync(dirname(registryPath), { recursive: true });
    const temporary = `${registryPath}.${process.pid}.tmp`;
    writeFileSync(temporary, `${JSON.stringify(value, null, 2)}\n`, "utf8");
    renameSync(temporary, registryPath);
}

function manifest(path) {
    const value = JSON.parse(readFileSync(join(path, "afterburner.json"), "utf8"));
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
    const visit = (current, relative = "") => {
        for (const name of readdirSync(current).sort()) {
            if (name === ".git" || name === "node_modules" || name === "bin" || name === "obj") continue;
            const absolute = join(current, name);
            const child = join(relative, name);
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

function installResolved(value, resolved, { preserve, expectedId } = {}) {
    try {
        const packageManifest = manifest(resolved.path);
        if (expectedId && packageManifest.id !== expectedId)
            throw new Error(`Source package changed identity from '${expectedId}' to '${packageManifest.id}'.`);
        const version = resolved.source.commit ?? resolved.source.version;
        const target = join(installedRoot, packageManifest.id, version);
        if (!existsSync(target)) {
            const staging = `${target}.staging-${process.pid}`;
            rmSync(staging, { recursive: true, force: true });
            mkdirSync(dirname(target), { recursive: true });
            cpSync(resolved.path, staging, { recursive: true, filter: source => basename(source) !== ".git" });
            renameSync(staging, target);
        }
        const previous = value.extensions[packageManifest.id];
        value.extensions[packageManifest.id] = {
            enabled: preserve ? previous.enabled : false,
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

function updateOne(value, id) {
    const current = value.extensions[id];
    if (!current) throw new Error(`Unknown Afterburner extension '${id}'.`);
    const result = installResolved(value, sourcePath(sourceSpec(current.source)), { preserve: true, expectedId: id });
    console.log(result.changed ? `Updated '${id}' to ${basename(result.target)}.` : `'${id}' is already current.`);
}

const [command, argument] = process.argv.slice(2);
const value = registry();
if (command === "install") {
    const result = installResolved(value, sourcePath(argument));
    console.log(`Installed Afterburner extension '${result.id}' disabled at ${basename(result.target)}.`);
    console.log("This package executes trusted JavaScript inside the Copilot process. Inspect it before enabling.");
} else if (command === "update") {
    if (argument === "--all") for (const id of Object.keys(value.extensions)) updateOne(value, id);
    else updateOne(value, argument);
} else if (command === "rollback") {
    const entry = value.extensions[argument];
    if (!entry) throw new Error(`Unknown Afterburner extension '${argument}'.`);
    if (!entry.previousActivePath || !existsSync(entry.previousActivePath))
        throw new Error(`No rollback version is available for '${argument}'.`);
    [entry.activePath, entry.previousActivePath] = [entry.previousActivePath, entry.activePath];
    entry.manifest = manifest(entry.activePath);
    entry.updatedAt = new Date().toISOString();
    save(value);
    console.log(`Rolled back '${argument}' to ${basename(entry.activePath)}.`);
} else if (command === "enable" || command === "disable") {
    if (!value.extensions[argument]) throw new Error(`Unknown Afterburner extension '${argument}'.`);
    value.extensions[argument].enabled = command === "enable";
    save(value);
    console.log(`${command === "enable" ? "Enabled" : "Disabled"} '${argument}'.`);
} else if (command === "list") {
    for (const [id, entry] of Object.entries(value.extensions))
        console.log(`${entry.enabled ? "enabled " : "disabled"} ${id} (${entry.manifest.visibility ?? "private"}) ${basename(entry.activePath)}`);
} else if (command === "inspect") {
    if (!value.extensions[argument]) throw new Error(`Unknown Afterburner extension '${argument}'.`);
    console.log(JSON.stringify(value.extensions[argument], null, 2));
} else {
    throw new Error("Usage: extension-manager.mjs install <source> | update <id>|--all | rollback <id> | enable|disable|inspect <id> | list");
}
