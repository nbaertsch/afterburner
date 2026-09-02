import { access, mkdir, open, stat } from "node:fs/promises";
import { dirname, join } from "node:path";
import { AtomicJsonState } from "./state.mjs";

function validTailState(value) {
    return value && value.version === 1 && typeof value.pathRef === "string" &&
        Number.isSafeInteger(value.offset) && value.offset >= 0 &&
        (value.fileRef === null || value.fileRef === undefined || typeof value.fileRef === "string");
}

export class NativeEventsTailer {
    constructor({
        root, path, sessionId, processId = process.pid, config, reference, recoverOffset,
        onEvent, onDiagnostic = async () => {}
    }) {
        this.root = root;
        this.path = path;
        this.sessionId = sessionId;
        this.processId = processId;
        this.config = config;
        this.reference = reference;
        this.recoverOffset = recoverOffset;
        this.onEvent = onEvent;
        this.onDiagnostic = onDiagnostic;
        this.pathRef = reference("path", path);
        this.statePath = join(root, "state", `native-${this.pathRef.slice(4)}.json`);
        this.offset = 0;
        this.readPosition = 0;
        this.pending = Buffer.alloc(0);
        this.discardingOversize = false;
        this.oversizeStart = null;
        this.fileRef = null;
        this.running = false;
        this.timer = null;
        this.polling = null;
        this.initialized = false;
        this.hadState = false;
        this.firstFileObservation = true;
        this.stats = {
            eventsRead: 0,
            corruptLines: 0,
            oversizeLines: 0,
            truncations: 0,
            rotations: 0,
            readErrors: 0,
            stateRecoveries: 0,
            lastReadAt: null,
            waitingForFile: false
        };
        this.state = new AtomicJsonState(this.statePath, {
            validate: value => validTailState(value) && value.pathRef === this.pathRef,
            recover: async () => ({
                version: 1,
                pathRef: this.pathRef,
                fileRef: null,
                offset: await recoverOffset(this.pathRef),
                recoveredAt: new Date().toISOString()
            })
        });
    }

    async initialize() {
        await mkdir(dirname(this.statePath), { recursive: true });
        try { await access(this.statePath); this.hadState = true; }
        catch { this.hadState = false; }
        const value = await this.state.load();
        this.offset = value.offset;
        this.readPosition = value.offset;
        this.fileRef = value.fileRef ?? null;
        if (this.state.recovered) this.stats.stateRecoveries++;
        this.initialized = true;
        return this;
    }

    async #save() {
        await this.state.save({
            version: 1,
            pathRef: this.pathRef,
            fileRef: this.fileRef,
            offset: this.offset,
            stats: { ...this.stats },
            updatedAt: new Date().toISOString()
        });
    }

    async #fileMetadata() {
        try {
            const metadata = await stat(this.path);
            return {
                size: metadata.size,
                fileRef: this.reference("file", `${metadata.dev}:${metadata.ino}:${metadata.birthtimeMs}`)
            };
        } catch (error) {
            if (error?.code === "ENOENT") return null;
            throw error;
        }
    }

    async #resetForFile(file, code) {
        this.offset = this.config.startPosition === "end" && this.firstFileObservation && !this.hadState
            ? file.size
            : 0;
        this.readPosition = this.offset;
        this.pending = Buffer.alloc(0);
        this.discardingOversize = false;
        this.oversizeStart = null;
        this.fileRef = file.fileRef;
        if (code) await this.onDiagnostic(code, { pathRef: this.pathRef, byteStart: 0, byteEnd: 0 });
        await this.#save();
    }

    async poll() {
        if (!this.initialized) await this.initialize();
        if (this.polling) return this.polling;
        this.polling = this.#pollOnce().finally(() => { this.polling = null; });
        return this.polling;
    }

    async #pollOnce() {
        let file;
        try {
            file = await this.#fileMetadata();
        } catch {
            this.stats.readErrors++;
            await this.onDiagnostic("native-stat-failed", { pathRef: this.pathRef });
            return 0;
        }
        if (!file) {
            this.stats.waitingForFile = true;
            return 0;
        }
        this.stats.waitingForFile = false;
        if (!this.fileRef) {
            if (this.offset > 0) {
                this.fileRef = file.fileRef;
                this.readPosition = this.offset;
                await this.#save();
            } else {
                await this.#resetForFile(file, null);
            }
        } else if (this.fileRef !== file.fileRef) {
            this.stats.rotations++;
            await this.#resetForFile(file, "native-file-rotated");
        } else if (file.size < this.readPosition || file.size < this.offset) {
            this.stats.truncations++;
            await this.#resetForFile(file, "native-file-truncated");
        }
        this.firstFileObservation = false;

        let processed = 0;
        let progressed = false;
        let handle;
        try {
            handle = await open(this.path, "r");
            while (processed < this.config.maxEventsPerPoll) {
                if (this.discardingOversize) {
                    if (this.readPosition >= file.size) break;
                    const length = Math.min(this.config.maxReadBytes, file.size - this.readPosition);
                    const buffer = Buffer.allocUnsafe(length);
                    const chunkStart = this.readPosition;
                    const { bytesRead } = await handle.read(buffer, 0, length, chunkStart);
                    if (bytesRead === 0) break;
                    const chunk = buffer.subarray(0, bytesRead);
                    const newline = chunk.indexOf(0x0a);
                    this.readPosition += bytesRead;
                    if (newline === -1) continue;
                    const lineEnd = chunkStart + newline + 1;
                    this.offset = lineEnd;
                    this.pending = chunk.subarray(newline + 1);
                    this.discardingOversize = false;
                    this.stats.oversizeLines++;
                    progressed = true;
                    await this.onDiagnostic("native-line-oversize", {
                        pathRef: this.pathRef,
                        byteStart: this.oversizeStart ?? lineEnd,
                        byteEnd: lineEnd
                    });
                    this.oversizeStart = null;
                    processed++;
                    continue;
                }

                let newline = this.pending.indexOf(0x0a);
                if (newline === -1) {
                    if (this.pending.length > this.config.maxLineBytes) {
                        this.oversizeStart = this.offset;
                        this.pending = Buffer.alloc(0);
                        this.discardingOversize = true;
                        continue;
                    }
                    if (this.readPosition >= file.size) break;
                    const length = Math.min(this.config.maxReadBytes, file.size - this.readPosition);
                    const buffer = Buffer.allocUnsafe(length);
                    const { bytesRead } = await handle.read(buffer, 0, length, this.readPosition);
                    if (bytesRead === 0) break;
                    this.readPosition += bytesRead;
                    this.pending = Buffer.concat([this.pending, buffer.subarray(0, bytesRead)]);
                    newline = this.pending.indexOf(0x0a);
                    if (newline === -1) continue;
                }

                const lineStart = this.offset;
                const lineEnd = this.offset + newline + 1;
                if (newline > this.config.maxLineBytes) {
                    this.pending = this.pending.subarray(newline + 1);
                    this.offset = lineEnd;
                    this.stats.oversizeLines++;
                    progressed = true;
                    processed++;
                    await this.onDiagnostic("native-line-oversize", {
                        pathRef: this.pathRef,
                        byteStart: lineStart,
                        byteEnd: lineEnd
                    });
                    continue;
                }
                const line = this.pending.subarray(0, newline);
                this.pending = this.pending.subarray(newline + 1);
                this.offset = lineEnd;
                progressed = true;
                if (line.length === 0) continue;
                try {
                    const event = JSON.parse(line.toString("utf8"));
                    await this.onEvent(event, {
                        kind: "native-events",
                        processId: this.processId,
                        sessionId: this.sessionId,
                        path: this.path,
                        byteStart: lineStart,
                        byteEnd: lineEnd
                    });
                    this.stats.eventsRead++;
                } catch {
                    this.stats.corruptLines++;
                    await this.onDiagnostic("native-line-corrupt", {
                        pathRef: this.pathRef,
                        byteStart: lineStart,
                        byteEnd: lineEnd
                    });
                }
                processed++;
            }
        } catch {
            this.stats.readErrors++;
            await this.onDiagnostic("native-read-failed", { pathRef: this.pathRef });
        } finally {
            await handle?.close().catch(() => {});
        }
        if (progressed) {
            this.stats.lastReadAt = new Date().toISOString();
            await this.#save().catch(async () => {
                this.stats.readErrors++;
                await this.onDiagnostic("native-state-write-failed", { pathRef: this.pathRef });
            });
        }
        return processed;
    }

    start() {
        if (this.running) return;
        this.running = true;
        const tick = async () => {
            if (!this.running) return;
            await this.poll().catch(() => { this.stats.readErrors++; });
            if (!this.running) return;
            this.timer = setTimeout(tick, this.config.pollIntervalMs);
            this.timer.unref?.();
        };
        this.timer = setTimeout(tick, 0);
        this.timer.unref?.();
    }

    async stop() {
        this.running = false;
        if (this.timer) clearTimeout(this.timer);
        if (this.polling) await this.polling;
        await this.#save().catch(() => {});
    }

    status() {
        return {
            enabled: true,
            pathRef: this.pathRef,
            offset: this.offset,
            pendingBytes: this.pending.length,
            discardingOversize: this.discardingOversize,
            ...this.stats
        };
    }
}
