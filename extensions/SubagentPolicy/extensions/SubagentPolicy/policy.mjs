export const exposureModes = new Set(["final-only", "status-and-final", "detailed"]);
const contextTiers = new Set(["inherit", "default", "long_context"]);
const modelPolicies = new Set(["preferred", "required"]);

const builtinPolicies = {
    solo: {
        displayName: "Solo",
        description: "Disable delegated built-in workers.",
        maxConcurrency: 1,
        maxDepth: 1,
        resultExposure: "final-only",
        disabledSubagents: ["explore", "task", "general-purpose", "code-review", "research", "rubber-duck"],
        agents: {},
        agentFactories: {}
    },
    conservative: {
        displayName: "Conservative",
        description: "Two shallow workers with final-result visibility.",
        maxConcurrency: 2,
        maxDepth: 1,
        resultExposure: "final-only",
        disabledSubagents: [],
        agents: {},
        agentFactories: {}
    },
    balanced: {
        displayName: "Balanced",
        description: "Four shallow workers with aggregate status.",
        maxConcurrency: 4,
        maxDepth: 1,
        resultExposure: "status-and-final",
        disabledSubagents: [],
        agents: {},
        agentFactories: {}
    },
    burst: {
        displayName: "Burst",
        description: "Six shallow workers for short bursts.",
        maxConcurrency: 6,
        maxDepth: 1,
        resultExposure: "status-and-final",
        disabledSubagents: [],
        agents: {},
        agentFactories: {}
    }
};

function nonEmpty(value, field) {
    if (typeof value !== "string" || !value.trim()) throw new Error(`${field} must be a non-empty string`);
    return value.trim();
}

function positiveInteger(value, field) {
    if (!Number.isInteger(value) || value < 1 || value > 128) {
        throw new Error(`${field} must be an integer from 1 through 128`);
    }
    return value;
}

function validateAgent(name, value) {
    if (!value || typeof value !== "object" || Array.isArray(value)) {
        throw new Error(`agents.${name} must be an object`);
    }
    const allowed = new Set(["model", "modelPolicy", "effortLevel", "contextTier", "autoInvoke"]);
    for (const key of Object.keys(value)) if (!allowed.has(key)) throw new Error(`agents.${name}.${key} is not supported`);
    const result = {};
    if (value.model !== undefined) result.model = nonEmpty(value.model, `agents.${name}.model`);
    if (value.modelPolicy !== undefined) {
        if (!modelPolicies.has(value.modelPolicy)) throw new Error(`agents.${name}.modelPolicy is invalid`);
        result.modelPolicy = value.modelPolicy;
    }
    if (value.effortLevel !== undefined) result.effortLevel = nonEmpty(value.effortLevel, `agents.${name}.effortLevel`);
    if (value.contextTier !== undefined) {
        if (!contextTiers.has(value.contextTier)) throw new Error(`agents.${name}.contextTier is invalid`);
        result.contextTier = value.contextTier;
    }
    if (value.autoInvoke !== undefined) {
        if (typeof value.autoInvoke !== "boolean") throw new Error(`agents.${name}.autoInvoke must be a boolean`);
        result.autoInvoke = value.autoInvoke;
    }
    return result;
}

export function validatePolicy(id, value) {
    if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error(`policies.${id} must be an object`);
    const allowed = new Set(["displayName", "description", "maxConcurrency", "maxDepth", "resultExposure", "disabledSubagents", "agents", "agentFactories"]);
    for (const key of Object.keys(value)) if (!allowed.has(key)) throw new Error(`policies.${id}.${key} is not supported`);
    const disabled = value.disabledSubagents ?? [];
    if (!Array.isArray(disabled) || disabled.some(item => typeof item !== "string" || !item.trim())) {
        throw new Error(`policies.${id}.disabledSubagents must contain non-empty strings`);
    }
    if (new Set(disabled).size !== disabled.length) throw new Error(`policies.${id}.disabledSubagents contains duplicates`);
    if (!exposureModes.has(value.resultExposure)) throw new Error(`policies.${id}.resultExposure is invalid`);
    const agents = {};
    for (const [name, settings] of Object.entries(value.agents ?? {})) {
        agents[nonEmpty(name, `policies.${id}.agent name`)] = validateAgent(name, settings);
    }
    const agentFactories = value.agentFactories ?? {};
    if (!agentFactories || typeof agentFactories !== "object" || Array.isArray(agentFactories)) {
        throw new Error(`policies.${id}.agentFactories must be an object`);
    }
    const factoryAllowed = new Set(["maxConcurrentSubagents", "maxTotalSubagents", "timeoutSeconds", "maxAiCredits"]);
    for (const key of Object.keys(agentFactories)) {
        if (!factoryAllowed.has(key)) throw new Error(`policies.${id}.agentFactories.${key} is not supported`);
        if (!Number.isInteger(agentFactories[key]) || agentFactories[key] < 1) {
            throw new Error(`policies.${id}.agentFactories.${key} must be a positive integer`);
        }
    }
    return {
        displayName: nonEmpty(value.displayName, `policies.${id}.displayName`),
        description: nonEmpty(value.description, `policies.${id}.description`),
        maxConcurrency: positiveInteger(value.maxConcurrency, `policies.${id}.maxConcurrency`),
        maxDepth: positiveInteger(value.maxDepth, `policies.${id}.maxDepth`),
        resultExposure: value.resultExposure,
        disabledSubagents: [...disabled],
        agents,
        agentFactories: { ...agentFactories }
    };
}

export function loadPolicyConfig(value = {}) {
    if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error("configuration must be an object");
    const allowed = new Set(["version", "defaultPolicy", "policies"]);
    for (const key of Object.keys(value)) if (!allowed.has(key)) throw new Error(`${key} is not supported`);
    if (value.version !== undefined && value.version !== 1) throw new Error("version must be 1");
    const policies = structuredClone(builtinPolicies);
    for (const [id, policy] of Object.entries(value.policies ?? {})) {
        policies[nonEmpty(id, "policy id")] = validatePolicy(id, policy);
    }
    const defaultPolicy = value.defaultPolicy ?? null;
    if (defaultPolicy !== null && !policies[defaultPolicy]) throw new Error(`defaultPolicy ${defaultPolicy} is unknown`);
    return { version: 1, defaultPolicy, policies };
}

export function sdkSettings(policy) {
    return {
        agents: structuredClone(policy.agents),
        disabledSubagents: [...policy.disabledSubagents],
        maxConcurrency: policy.maxConcurrency,
        maxDepth: policy.maxDepth
    };
}
