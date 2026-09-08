import { existsSync } from "node:fs";
import { join } from "node:path";

export const CANONICAL_ID = "openai-server";
export const LEGACY_ID = "copilot-openai";
export const CANONICAL_TITLE = "OpenAI Server";
export const LEGACY_TITLE = "Copilot OpenAI Bridge";
export const CANONICAL_HEALTH_PATH = "/__afterburner/openai-server/health";
export const LEGACY_HEALTH_PATH = "/__afterburner/copilot-openai/health";
export const CANONICAL_HEALTH_MARKER = "afterburner-openai-server-v1";
export const LEGACY_HEALTH_MARKER = "afterburner-copilot-openai-bridge-v1";

export function afterburnerHome(env = process.env) {
    return env.AFTERBURNER_HOME ?? join(env.USERPROFILE ?? "", ".afterburner");
}

export function canonicalConfigPath(env = process.env) {
    return join(afterburnerHome(env), "config", "openai-server.json");
}

export function legacyConfigPath(env = process.env) {
    return join(afterburnerHome(env), "config", "copilot-openai.json");
}

export function configuredPathCandidates(env = process.env) {
    const canonicalEnv = env.AFTERBURNER_OPENAI_SERVER_CONFIG?.trim();
    const legacyEnv = env.AFTERBURNER_COPILOT_OPENAI_CONFIG?.trim();
    return [canonicalEnv, legacyEnv, canonicalConfigPath(env), legacyConfigPath(env)]
        .filter((value, index, values) => value && values.indexOf(value) === index);
}

export function displayConfigPath(env = process.env) {
    return configuredPathCandidates(env).find(path => existsSync(path)) ?? configuredPathCandidates(env)[0];
}

export function firstConfiguredEnv(env = process.env) {
    return env.AFTERBURNER_OPENAI_SERVER_CONFIG?.trim() || env.AFTERBURNER_COPILOT_OPENAI_CONFIG?.trim() || undefined;
}
