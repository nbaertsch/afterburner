import { readFileSync } from "node:fs";
import { join } from "node:path";

export const COPILOT_VERSION = "1.0.84-4";

function replaceOnce(source, search, replacement, label) {
    const count = source.split(search).length - 1;
    if (count !== 1) throw new Error(`Subagent Policy ${COPILOT_VERSION} transform failed at ${label}: expected 1 anchor, found ${count}`);
    return source.replace(search, replacement);
}

export function installNativeSubagentPolicy(source, config) {
    const presets = Object.entries(config.policies ?? {}).map(([id, policy]) => ({
        id,
        label: policy.displayName,
        description: `${policy.description} · max ${policy.maxConcurrency} concurrent · depth ${policy.maxDepth}`,
        subagents: {
            ...(policy.agents ? { agents: policy.agents } : {}),
            maxDepth: policy.maxDepth,
            resultExposure: policy.resultExposure,
            maxConcurrency: policy.maxConcurrency
        }
    }));
    if (!presets.length) throw new Error("Subagent Policy requires at least one policy preset");
    const encoded = JSON.stringify(presets);
    const componentAnchor = 'sdn=({builtInAgents:e,customAgents:t,subagentSettings:n,builtInDeclaredModels:r,sessionModel:o,complementaryModelAvailable:s=!1,onSelect:a,onToggleEnabled:l,onReset:c,onCancel:d})=>';
    const componentReplacement = 'sdn=({builtInAgents:e,customAgents:t,subagentSettings:n,builtInDeclaredModels:r,sessionModel:o,complementaryModelAvailable:s=!1,onSelect:a,onToggleEnabled:l,onReset:c,onCancel:d,afterburnPolicies:$abPolicies=[],onApplyAfterburnPolicy:$abApply})=>';
    source = replaceOnce(source, componentAnchor, componentReplacement, "native /subagents component parameters");
    const rowsAnchor = 'R=C.map(U=>({label:U.displayName,value:HXr(U),projection:U})),';
    const rowsReplacement = 'R=[...C.map(U=>({label:U.displayName,value:HXr(U),projection:U})),...$abPolicies.map(U=>({label:`Policy: ${U.label}`,value:{kind:"afterburn-policy",policy:U},projection:{displayName:`Policy: ${U.label}`,origin:"afterburner",modelPolicy:"required",modelText:`max ${U.subagents.maxConcurrency} · depth ${U.subagents.maxDepth}`,modelSuffix:"",modelDim:!1,modelWarn:!1,overridden:!1,disableable:!1,disabled:!1}}))],';
    source = replaceOnce(source, rowsAnchor, rowsReplacement, "native /subagents policy rows");
    const selectAnchor = 'onSelect:U=>a(U.value),onEscape:d';
    const selectReplacement = 'onSelect:U=>U.value?.kind==="afterburn-policy"?$abApply?.(U.value.policy):a(U.value),onEscape:d';
    source = replaceOnce(source, selectAnchor, selectReplacement, "native /subagents policy selection");
    const pickerAnchor = 'if(t)return RD.default.createElement(sdn,{builtInAgents:';
    const pickerReplacement = `if(t){let $abPolicies=${encoded};return RD.default.createElement(sdn,{builtInAgents:`;
    source = replaceOnce(source, pickerAnchor, pickerReplacement, "native /subagents picker");
    const pickerEnd = 'onCancel:()=>a(!1)});let se=re=>';
    const policyUI = `onCancel:()=>a(!1),afterburnPolicies:$abPolicies,onApplyAfterburnPolicy:J=>{D.current=D.current.then(async()=>{await R.tools.updateSubagentSettings({settings:J.subagents}),N(ee=>({...ee,subagents:J.subagents})),O({type:"info",text:\`Applied subagent policy \${J.label}: max \${J.subagents.maxConcurrency} concurrent, depth \${J.subagents.maxDepth}\`})}).catch(ee=>O({type:"error",text:\`Failed to apply subagent policy: \${y.errorFormattingFormatUnknown(ee)}\`}))}});}let se=re=>`;
    return replaceOnce(source, pickerEnd, policyUI, "native /subagents policy controls");
}

export async function activate({ registerAppSourceTransform }) {
    const path = process.env.AFTERBURNER_SUBAGENT_POLICY_CONFIG ??
        join(process.env.AFTERBURNER_HOME ?? join(process.env.USERPROFILE ?? "", ".afterburner"), "config", "subagent-policy.json");
    let config;
    try {
        config = JSON.parse(readFileSync(path, "utf8"));
    } catch (error) {
        if (error?.code !== "ENOENT") throw error;
        config = {
            policies: {
                "luna-three": {
                    displayName: "Luna Three",
                    description: "Route exploration through Luna with bounded parallelism",
                    maxConcurrency: 3,
                    maxDepth: 1,
                    resultExposure: "summary",
                    agents: { explore: { model: "luna" } }
                }
            }
        };
    }
    registerAppSourceTransform(source => installNativeSubagentPolicy(source, config));
}
