import { join } from "node:path";

export const CANONICAL_ID = "subagent-policy";
export const CANONICAL_TITLE = "Subagent Policy";
export const configPath = (env = process.env) =>
    env.AFTERBURNER_SUBAGENT_POLICY_CONFIG ??
    join(env.AFTERBURNER_HOME ?? join(env.USERPROFILE ?? env.HOME, ".afterburner"), "config", "subagent-policy.json");
