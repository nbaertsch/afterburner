import { mkdir, readFile, rename, rm, writeFile } from "node:fs/promises";
import { basename, dirname, join } from "node:path";
import { randomBytes } from "node:crypto";

function suffix() {
    return `${process.pid}-${Date.now()}-${randomBytes(4).toString("hex")}`;
}

export async function atomicWriteJson(path, value) {
    await mkdir(dirname(path), { recursive: true });
    const temporary = `${path}.${suffix()}.tmp`;
    const backup = `${path}.${suffix()}.bak`;
    await writeFile(temporary, `${JSON.stringify(value, null, 2)}\n`, "utf8");
    try {
        await rename(temporary, path);
    } catch (error) {
        if (error?.code !== "EEXIST" && error?.code !== "EPERM") {
            await rm(temporary, { force: true });
            throw error;
        }
        let backedUp = false;
        try {
            await rename(path, backup);
            backedUp = true;
        } catch (backupError) {
            if (backupError?.code !== "ENOENT") throw backupError;
        }
        try {
            await rename(temporary, path);
            if (backedUp) await rm(backup, { force: true });
        } catch (replaceError) {
            if (backedUp) await rename(backup, path).catch(() => {});
            await rm(temporary, { force: true });
            throw replaceError;
        }
    }
}

export async function quarantineCorruptFile(path) {
    const corruptDirectory = join(dirname(path), "corrupt");
    await mkdir(corruptDirectory, { recursive: true });
    const target = join(corruptDirectory, `${basename(path)}.${suffix()}.corrupt`);
    try {
        await rename(path, target);
        return target;
    } catch (error) {
        if (error?.code === "ENOENT") return null;
        throw error;
    }
}

export class AtomicJsonState {
    constructor(path, { validate = () => true, recover = async () => ({}) } = {}) {
        this.path = path;
        this.validate = validate;
        this.recover = recover;
        this.value = undefined;
        this.recovered = false;
        this.corruptPath = null;
        this.saveChain = Promise.resolve();
    }

    async load() {
        try {
            const value = JSON.parse(await readFile(this.path, "utf8"));
            if (!this.validate(value)) throw new Error("invalid-state-shape");
            this.value = value;
        } catch (error) {
            if (error?.code !== "ENOENT") {
                this.corruptPath = await quarantineCorruptFile(this.path).catch(() => null);
                this.recovered = true;
            }
            this.value = await this.recover();
        }
        return this.value;
    }

    async save(value) {
        this.value = value;
        const operation = this.saveChain.catch(() => {}).then(() => atomicWriteJson(this.path, value));
        this.saveChain = operation;
        return operation;
    }
}
