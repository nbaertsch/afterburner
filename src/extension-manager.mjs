import { execFileSync } from "node:child_process";
import { cpSync, existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
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
    writeFileSync(registryPath, `${JSON.stringify(value, null, 2)}\n`, "utf8");
}

function manifest(path) {
    const value = JSON.parse(readFileSync(join(path, "afterburner.json"), "utf8"));
    if (value.schemaVersion !== 1 || !/^[a-z0-9][a-z0-9-]{0,63}$/.test(value.id) ||
        value.runtime?.execution !== "in-process" || typeof value.runtime?.entrypoint !== "string")
        throw new Error("Invalid afterburner.json manifest.");
    if (!existsSync(join(path, value.runtime.entrypoint))) throw new Error("Runtime entrypoint does not exist.");
    return value;
}

function sourcePath(spec) {
    const local = resolve(spec);
    if (existsSync(join(local, "afterburner.json"))) return { path: local, source: { type: "path", value: local } };
    const temporary = join(root, "staging", `${Date.now()}-${basename(spec).replace(/[^A-Za-z0-9.-]/g, "_")}`);
    mkdirSync(dirname(temporary), { recursive: true });
    const url = /^[\w.-]+\/[\w.-]+(?:@.+)?$/.test(spec)
        ? `git@github.com:${spec.split("@")[0]}.git`
        : spec;
    const ref = /^[\w.-]+\/[\w.-]+@(.+)$/.exec(spec)?.[1];
    execFileSync("git", ["clone", "--quiet", ...(ref ? ["--branch", ref] : []), url, temporary], { stdio: "inherit" });
    const commit = execFileSync("git", ["-C", temporary, "rev-parse", "HEAD"], { encoding: "utf8" }).trim();
    return { path: temporary, source: { type: "git", value: url, ref: ref ?? null, commit } };
}

const [command, argument] = process.argv.slice(2);
const value = registry();
if (command === "install") {
    const resolved = sourcePath(argument);
    const packageManifest = manifest(resolved.path);
    const target = join(installedRoot, packageManifest.id, resolved.source.commit ?? "local");
    rmSync(target, { recursive: true, force: true });
    cpSync(resolved.path, target, { recursive: true, filter: source => basename(source) !== ".git" });
    value.extensions[packageManifest.id] = {
        enabled: false, activePath: target, manifest: packageManifest, source: resolved.source
    };
    save(value);
    console.log(`Installed Afterburner extension '${packageManifest.id}' disabled.`);
    console.log("This package executes trusted JavaScript inside the Copilot process. Inspect it before enabling.");
} else if (command === "enable" || command === "disable") {
    if (!value.extensions[argument]) throw new Error(`Unknown Afterburner extension '${argument}'.`);
    value.extensions[argument].enabled = command === "enable";
    save(value);
    console.log(`${command === "enable" ? "Enabled" : "Disabled"} '${argument}'.`);
} else if (command === "list") {
    for (const [id, entry] of Object.entries(value.extensions))
        console.log(`${entry.enabled ? "enabled " : "disabled"} ${id} (${entry.manifest.visibility ?? "private"})`);
} else if (command === "inspect") {
    if (!value.extensions[argument]) throw new Error(`Unknown Afterburner extension '${argument}'.`);
    console.log(JSON.stringify(value.extensions[argument], null, 2));
} else {
    throw new Error("Usage: extension-manager.mjs install <path|git-url|owner/repo@ref> | enable|disable|inspect <id> | list");
}
