import { access, appendFile, mkdir, readFile, readdir, rm, stat } from "node:fs/promises";
import { constants as fsConstants, createReadStream } from "node:fs";
import { createInterface } from "node:readline";
import { basename, join } from "node:path";
import { AtomicJsonState } from "./state.mjs";
import { sanitizeStoredRecord } from "./schema.mjs";

const SEGMENT_PATTERN = /^segment-(\d{8})\.jsonl$/;

function processKey(value) {
    return String(value).replace(/[^A-Za-z0-9_-]+/g, "-").slice(0, 96) || `process-${process.pid}`;
}

async function existingSize(path) {
    try { return (await stat(path)).size; }
    catch (error) { if (error?.code === "ENOENT") return 0; throw error; }
}

async function listFiles(directory, predicate = () => true) {
    try {
        const entries = await readdir(directory, { withFileTypes: true });
        return entries.filter(entry => entry.isFile() && predicate(entry.name)).map(entry => join(directory, entry.name));
    } catch (error) {
        if (error?.code === "ENOENT") return [];
        throw error;
    }
}

async function listDirectories(directory) {
    try {
        const entries = await readdir(directory, { withFileTypes: true });
        return entries.filter(entry => entry.isDirectory()).map(entry => join(directory, entry.name));
    } catch (error) {
        if (error?.code === "ENOENT") return [];
        throw error;
    }
}

function validWriterState(value) {
    return value && value.version === 1 && typeof value.processKey === "string" &&
        Number.isSafeInteger(value.sequence) && value.sequence >= 0 &&
        Number.isSafeInteger(value.currentBytes) && value.currentBytes >= 0 &&
        value.counters && typeof value.counters === "object";
}

export async function listSegmentFiles(root) {
    const segmentsRoot = join(root, "segments");
    const directories = await listDirectories(segmentsRoot);
    const files = [];
    for (const directory of directories) {
        for (const path of await listFiles(directory, name => SEGMENT_PATTERN.test(name))) {
            const metadata = await stat(path).catch(() => null);
            if (metadata) files.push({ path, size: metadata.size, mtimeMs: metadata.mtimeMs, processDirectory: directory });
        }
    }
    return files.sort((left, right) => left.mtimeMs - right.mtimeMs || left.path.localeCompare(right.path));
}

export class SegmentedJsonlStore {
    constructor({ root, storage, queue, processId = process.pid, runId = Date.now().toString(36) }) {
        this.root = root;
        this.storageConfig = storage;
        this.queueConfig = queue;
        this.processKey = processKey(`p${processId}-${runId}`);
        this.segmentDirectory = join(root, "segments", this.processKey);
        this.statePath = join(root, "state", `writer-${this.processKey}.json`);
        this.queue = [];
        this.queueBytes = 0;
        this.drainPromise = null;
        this.closed = false;
        this.heartbeat = null;
        this.sequence = 0;
        this.currentBytes = 0;
        this.currentSegment = null;
        this.retention = { deletedSegments: 0, deletedBytes: 0, blockedBytes: 0 };
        this.counters = {
            writtenRecords: 0,
            writtenBytes: 0,
            droppedQueue: 0,
            droppedOversize: 0,
            droppedInvalid: 0,
            droppedWrite: 0,
            droppedBytes: 0,
            writeErrors: 0,
            stateRecoveries: 0
        };
        this.state = new AtomicJsonState(this.statePath, {
            validate: validWriterState,
            recover: () => this.#recoverWriterState()
        });
    }

    async initialize() {
        await mkdir(this.segmentDirectory, { recursive: true });
        const value = await this.state.load();
        this.sequence = value.sequence ?? 0;
        this.currentSegment = value.activeSegment ?? null;
        this.currentBytes = value.currentBytes ?? 0;
        this.counters = { ...this.counters, ...(value.counters ?? {}) };
        if (this.state.recovered) this.counters.stateRecoveries++;
        if (this.currentSegment) {
            const path = join(this.segmentDirectory, this.currentSegment);
            const size = await existingSize(path);
            this.currentBytes = size;
            if (size >= this.storageConfig.segmentBytes) this.currentSegment = null;
        }
        if (!this.currentSegment) await this.#openNextSegment();
        await this.#persistState();
        await this.applyRetention();
        this.heartbeat = setInterval(() => { this.#persistState().catch(() => {}); }, 10_000);
        this.heartbeat.unref?.();
        return this;
    }

    async #recoverWriterState() {
        await mkdir(this.segmentDirectory, { recursive: true });
        const files = await listFiles(this.segmentDirectory, name => SEGMENT_PATTERN.test(name));
        let sequence = 0;
        let activeSegment = null;
        for (const path of files) {
            const match = SEGMENT_PATTERN.exec(basename(path));
            const candidate = Number(match?.[1] ?? 0);
            if (candidate >= sequence) {
                sequence = candidate;
                activeSegment = basename(path);
            }
        }
        const currentBytes = activeSegment ? await existingSize(join(this.segmentDirectory, activeSegment)) : 0;
        if (currentBytes >= this.storageConfig.segmentBytes) activeSegment = null;
        return {
            version: 1,
            processKey: this.processKey,
            sequence,
            activeSegment,
            currentBytes: activeSegment ? currentBytes : 0,
            counters: { ...this.counters },
            recoveredAt: new Date().toISOString()
        };
    }

    async #openNextSegment() {
        this.sequence++;
        this.currentSegment = `segment-${String(this.sequence).padStart(8, "0")}.jsonl`;
        this.currentBytes = await existingSize(join(this.segmentDirectory, this.currentSegment));
    }

    #stateValue(activeSegment = this.currentSegment) {
        return {
            version: 1,
            processKey: this.processKey,
            pid: process.pid,
            sequence: this.sequence,
            activeSegment,
            currentBytes: this.currentBytes,
            queueRecords: this.queue.length,
            queueBytes: this.queueBytes,
            counters: { ...this.counters },
            retention: { ...this.retention },
            updatedAt: new Date().toISOString()
        };
    }

    async #persistState(activeSegment = this.currentSegment) {
        try {
            await this.state.save(this.#stateValue(activeSegment));
        } catch {
            this.counters.writeErrors++;
        }
    }

    enqueue(record) {
        if (this.closed) return false;
        const clean = sanitizeStoredRecord(record);
        if (!clean) {
            this.counters.droppedInvalid++;
            return false;
        }
        const line = `${JSON.stringify(clean)}\n`;
        const bytes = Buffer.byteLength(line, "utf8");
        if (bytes > this.storageConfig.maxRecordBytes || bytes > this.storageConfig.segmentBytes) {
            this.counters.droppedOversize++;
            this.counters.droppedBytes += bytes;
            return false;
        }
        if (this.queue.length >= this.queueConfig.maxRecords || this.queueBytes + bytes > this.queueConfig.maxBytes) {
            this.counters.droppedQueue++;
            this.counters.droppedBytes += bytes;
            return false;
        }
        this.queue.push({ line, bytes });
        this.queueBytes += bytes;
        this.#scheduleDrain();
        return true;
    }

    async append(record) {
        const accepted = this.enqueue(record);
        await this.flush();
        return accepted;
    }

    #scheduleDrain() {
        if (this.drainPromise) return;
        this.drainPromise = new Promise(resolve => setImmediate(resolve))
            .then(() => this.#drain())
            .finally(() => {
                this.drainPromise = null;
                if (this.queue.length > 0 && !this.closed) this.#scheduleDrain();
            });
    }

    async #drain() {
        while (this.queue.length > 0) {
            const item = this.queue.shift();
            this.queueBytes -= item.bytes;
            try {
                if (this.currentBytes > 0 && this.currentBytes + item.bytes > this.storageConfig.segmentBytes) {
                    await this.#persistState(null);
                    await this.#openNextSegment();
                    await this.applyRetention();
                }
                const path = join(this.segmentDirectory, this.currentSegment);
                await appendFile(path, item.line, "utf8");
                this.currentBytes += item.bytes;
                this.counters.writtenRecords++;
                this.counters.writtenBytes += item.bytes;
            } catch {
                this.counters.writeErrors++;
                this.counters.droppedWrite++;
                this.counters.droppedBytes += item.bytes;
            }
        }
        await this.applyRetention();
        await this.#persistState();
    }

    async flush() {
        while (this.queue.length > 0 || this.drainPromise) {
            if (!this.drainPromise && this.queue.length > 0) this.#scheduleDrain();
            if (this.drainPromise) await this.drainPromise;
        }
    }

    async close() {
        if (this.closed) return;
        await this.flush();
        this.closed = true;
        if (this.heartbeat) clearInterval(this.heartbeat);
        this.heartbeat = null;
        await this.#persistState(null);
        await this.applyRetention();
    }

    async #protectedSegments() {
        const protectedPaths = new Set();
        if (this.currentSegment && !this.closed) {
            protectedPaths.add(join(this.segmentDirectory, this.currentSegment));
        }
        for (const statePath of await listFiles(join(this.root, "state"), name => /^writer-.*\.json$/.test(name))) {
            try {
                const value = JSON.parse(await readFile(statePath, "utf8"));
                if (typeof value.activeSegment !== "string" || typeof value.processKey !== "string") continue;
                const updatedAt = Date.parse(value.updatedAt);
                const recentlyActive = Number.isFinite(updatedAt) && Date.now() - updatedAt < 30_000;
                let processActive = false;
                if (Number.isSafeInteger(value.pid) && value.pid > 0) {
                    try { process.kill(value.pid, 0); processActive = true; }
                    catch (error) { processActive = error?.code === "EPERM"; }
                }
                if (processActive && recentlyActive) {
                    protectedPaths.add(join(this.root, "segments", value.processKey, value.activeSegment));
                }
            } catch {}
        }
        return protectedPaths;
    }

    async applyRetention() {
        const files = await listSegmentFiles(this.root);
        const protectedPaths = await this.#protectedSegments(files);
        let total = files.reduce((sum, file) => sum + file.size, 0);
        let deletedSegments = 0;
        let deletedBytes = 0;
        for (const file of files) {
            if (total <= this.storageConfig.maxBytes) break;
            if (protectedPaths.has(file.path)) continue;
            try {
                await rm(file.path, { force: true });
                total -= file.size;
                deletedSegments++;
                deletedBytes += file.size;
            } catch {}
        }
        this.retention.deletedSegments += deletedSegments;
        this.retention.deletedBytes += deletedBytes;
        this.retention.blockedBytes = Math.max(0, total - this.storageConfig.maxBytes);
        return { totalBytes: total, deletedSegments, deletedBytes, blockedBytes: this.retention.blockedBytes };
    }

    async inventory() {
        const files = await listSegmentFiles(this.root);
        const states = await listFiles(join(this.root, "state"), name => /^writer-.*\.json$/.test(name));
        const corrupt = await listFiles(join(this.root, "state", "corrupt"));
        const aggregateCounters = { ...this.counters };
        for (const statePath of states) {
            if (statePath === this.statePath) continue;
            try {
                const value = JSON.parse(await readFile(statePath, "utf8"));
                for (const key of Object.keys(aggregateCounters)) {
                    if (Number.isFinite(value.counters?.[key])) aggregateCounters[key] += value.counters[key];
                }
            } catch {}
        }
        return {
            segmentCount: files.length,
            segmentBytes: files.reduce((sum, file) => sum + file.size, 0),
            writerCount: states.length,
            corruptStateCount: corrupt.length,
            queueRecords: this.queue.length,
            queueBytes: this.queueBytes,
            counters: aggregateCounters,
            retention: { ...this.retention }
        };
    }

    async readRecent(limit = 50, predicate = () => true) {
        const files = (await listSegmentFiles(this.root)).reverse();
        const records = [];
        for (const file of files) {
            let content;
            try { content = await readFile(file.path, "utf8"); }
            catch { continue; }
            const lines = content.split(/\r?\n/);
            for (let index = lines.length - 1; index >= 0; index--) {
                if (!lines[index]) continue;
                try {
                    const clean = sanitizeStoredRecord(JSON.parse(lines[index]));
                    if (clean && predicate(clean)) records.push(clean);
                } catch {}
                if (records.length >= limit) return records;
            }
        }
        return records;
    }

    async *iterateRecords() {
        for (const file of await listSegmentFiles(this.root)) {
            const input = createReadStream(file.path, { encoding: "utf8" });
            const lines = createInterface({ input, crlfDelay: Infinity });
            try {
                for await (const line of lines) {
                    if (!line) continue;
                    try {
                        const clean = sanitizeStoredRecord(JSON.parse(line));
                        if (clean) yield clean;
                    } catch {}
                }
            } finally {
                lines.close();
                input.destroy();
            }
        }
    }

    async recoverOffset(pathRef) {
        let offset = 0;
        for await (const record of this.iterateRecords()) {
            if (record.source.kind === "native-events" && record.source.pathRef === pathRef &&
                Number.isSafeInteger(record.source.byteEnd)) offset = Math.max(offset, record.source.byteEnd);
        }
        return offset;
    }

    async inspectIntegrity(maxFiles = 8) {
        const files = (await listSegmentFiles(this.root)).slice(-maxFiles);
        let checkedLines = 0;
        let invalidLines = 0;
        for (const file of files) {
            let content;
            try { content = await readFile(file.path, "utf8"); }
            catch { invalidLines++; continue; }
            for (const line of content.split(/\r?\n/)) {
                if (!line) continue;
                checkedLines++;
                try { if (!sanitizeStoredRecord(JSON.parse(line))) invalidLines++; }
                catch { invalidLines++; }
            }
        }
        let writable = true;
        try { await access(this.root, fsConstants.W_OK); }
        catch { writable = false; }
        return { writable, checkedFiles: files.length, checkedLines, invalidLines };
    }
}
