import { createHash, randomBytes } from "node:crypto";
import { mkdir, open, rename, rm, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { sanitizeStoredRecord } from "./schema.mjs";

function bundleName(now = new Date()) {
    return `black-box-export-${now.toISOString().replace(/[:.]/g, "-")}-${randomBytes(3).toString("hex")}`;
}

export async function exportSanitizedBundle({ root, store, maxRecords, now = () => new Date() }) {
    const createdAt = now();
    const exportsRoot = join(root, "exports");
    const name = bundleName(createdAt);
    const staging = join(exportsRoot, `.${name}.staging`);
    const target = join(exportsRoot, name);
    await mkdir(staging, { recursive: true });
    const timelinePath = join(staging, "timeline.jsonl");
    const timeline = await open(timelinePath, "w");
    const digest = createHash("sha256");
    const counts = { event: 0, milestone: 0, anomaly: 0, aggregate: 0, drop: 0 };
    const eventTypes = new Map();
    let recordCount = 0;
    let firstTimestamp = null;
    let lastTimestamp = null;
    let invalidRecordsSkipped = 0;
    let truncated = false;
    try {
        for await (const stored of store.iterateRecords()) {
            if (recordCount >= maxRecords) {
                truncated = true;
                break;
            }
            const record = sanitizeStoredRecord(stored);
            if (!record) {
                invalidRecordsSkipped++;
                continue;
            }
            const line = `${JSON.stringify(record)}\n`;
            await timeline.write(line, null, "utf8");
            digest.update(line);
            counts[record.kind]++;
            eventTypes.set(record.eventType, (eventTypes.get(record.eventType) ?? 0) + 1);
            firstTimestamp ??= record.timestamp;
            lastTimestamp = record.timestamp;
            recordCount++;
        }
        await timeline.sync();
        await timeline.close();

        const summary = {
            schemaVersion: 1,
            recordCount,
            counts,
            eventTypes: Object.fromEntries([...eventTypes].sort((left, right) => right[1] - left[1]).slice(0, 100))
        };
        await writeFile(join(staging, "summary.json"), `${JSON.stringify(summary, null, 2)}\n`, "utf8");
        const manifest = {
            schemaVersion: 1,
            format: "afterburner-black-box-export",
            createdAt: createdAt.toISOString(),
            recordCount,
            invalidRecordsSkipped,
            truncated,
            firstTimestamp,
            lastTimestamp,
            files: {
                timeline: { name: "timeline.jsonl", sha256: digest.digest("hex") },
                summary: { name: "summary.json" }
            },
            privacy: {
                metadataOnly: true,
                identifiersHashed: true,
                nativeSourcesReferencedByByteRange: true,
                bodiesIncluded: false
            }
        };
        await writeFile(join(staging, "manifest.json"), `${JSON.stringify(manifest, null, 2)}\n`, "utf8");
        await mkdir(exportsRoot, { recursive: true });
        await rename(staging, target);
        return { path: target, manifest };
    } catch (error) {
        await timeline.close().catch(() => {});
        await rm(staging, { recursive: true, force: true });
        throw error;
    }
}
