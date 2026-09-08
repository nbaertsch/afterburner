import { mkdir, readFile, writeFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const checkOnly = process.argv.includes("--check");
const copies = [
  ["src/app.js", "internal/runtimepkg/app.js"],
  ["src/runtime/afterburner-ui.mjs", "internal/runtimepkg/runtime/afterburner-ui.mjs"],
  ["src/runtime/afterburner-ui.mjs", "sdk/ui/dist/afterburner-ui.mjs"]
];

let stale = false;
for (const [sourceName, targetName] of copies) {
  const sourcePath = resolve(root, sourceName);
  const targetPath = resolve(root, targetName);
  const source = await readFile(sourcePath);
  let target;
  try {
    target = await readFile(targetPath);
  } catch (error) {
    if (error?.code !== "ENOENT") throw error;
  }

  if (target?.equals(source)) continue;
  stale = true;
  if (checkOnly) {
    process.stderr.write(`Stale generated runtime: ${targetName} (source: ${sourceName})\n`);
    continue;
  }
  await mkdir(dirname(targetPath), { recursive: true });
  await writeFile(targetPath, source);
  process.stdout.write(`Synced ${targetName}\n`);
}

if (checkOnly && stale) process.exitCode = 1;
