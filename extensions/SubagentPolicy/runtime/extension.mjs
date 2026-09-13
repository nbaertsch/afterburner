import { readFileSync } from "node:fs";
import { join } from "node:path";
import { loadPolicyConfig, sdkSettings } from "../extensions/SubagentPolicy/policy.mjs";

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
        subagents: sdkSettings(policy)
    }));
    if (!presets.length) throw new Error("Subagent Policy requires at least one policy preset");
    const encoded = JSON.stringify(presets);
    const componentAnchor = 'sdn=({builtInAgents:e,customAgents:t,subagentSettings:n,builtInDeclaredModels:r,sessionModel:o,complementaryModelAvailable:s=!1,onSelect:a,onToggleEnabled:l,onReset:c,onCancel:d})=>';
    const componentReplacement = 'sdn=({builtInAgents:e,customAgents:t,subagentSettings:n,builtInDeclaredModels:r,sessionModel:o,complementaryModelAvailable:s=!1,onSelect:a,onToggleEnabled:l,onReset:c,onCancel:d,afterburnPolicies:$abPolicies=[],afterburnModelIDs:$abModelIDs=new Set(),onApplyAfterburnPolicy:$abApply})=>';
    source = replaceOnce(source, componentAnchor, componentReplacement, "native /subagents component parameters");
    const rowsAnchor = 'R=C.map(U=>({label:U.displayName,value:HXr(U),projection:U})),';
    const rowsReplacement = 'R=[...C.map(U=>({label:U.displayName,value:HXr(U),projection:U})),...$abPolicies.map(U=>{let W=[...new Set(Object.values(U.subagents.agents??{}).map(V=>V?.model).filter(V=>typeof V==="string"&&!["default","inherit","complementary"].includes(V)))],V=W.filter(K=>!$abModelIDs.has(K));return{label:`Policy: ${U.label}`,value:{kind:"afterburn-policy",policy:U},projection:{displayName:`Policy: ${U.label}`,origin:"afterburner",modelPolicy:"required",modelText:V.length>0?"unavailable":`max ${U.subagents.maxConcurrency} · depth ${U.subagents.maxDepth}`,modelSuffix:V.length>0?` (${V.join(", ")})`:"",modelDim:V.length>0,modelWarn:V.length>0,overridden:!1,disableable:!1,disabled:V.length>0}}})],';
    source = replaceOnce(source, rowsAnchor, rowsReplacement, "native /subagents policy rows");
    const selectAnchor = 'onSelect:U=>a(U.value),onEscape:d';
    const selectReplacement = 'onSelect:U=>U.value?.kind==="afterburn-policy"?$abApply?.(U.value.policy):a(U.value),onEscape:d';
    source = replaceOnce(source, selectAnchor, selectReplacement, "native /subagents policy selection");
    const pickerAnchor = 'if(t)return RD.default.createElement(sdn,{builtInAgents:';
    const pickerReplacement = `if(t){let $abPolicies=${encoded},$abModels=h?.type==="success"?h.list:[],$abModelIDs=new Set($abModels.flatMap(U=>{let W=U?.provider??U?.providerId,V=U?.providerModelId??U?.modelId;return[U?.id,U?.selectionId,typeof W==="string"&&typeof V==="string"?\`\${W}/\${V}\`:null].filter(K=>typeof K==="string"&&K.length>0)}));return RD.default.createElement(sdn,{builtInAgents:`;
    source = replaceOnce(source, pickerAnchor, pickerReplacement, "native /subagents picker");
    const pickerEnd = 'onCancel:()=>a(!1)});let se=re=>';
    const policyUI = `onCancel:()=>a(!1),afterburnPolicies:$abPolicies,afterburnModelIDs:$abModelIDs,onApplyAfterburnPolicy:J=>{D.current=D.current.then(async()=>{if(!Array.isArray($abModels)||$abModels.length===0)throw new Error("Copilot model catalog is unavailable");let ee=[...new Set(Object.values(J.subagents.agents??{}).map(ne=>ne?.model).filter(ne=>typeof ne==="string"&&!["default","inherit","complementary"].includes(ne)))],ne=ee.filter(te=>!$abModelIDs.has(te));if(ne.length>0)throw new Error(\`unavailable Copilot model ID(s): \${ne.join(", ")}\`);let te=await R.tools.updateSubagentSettings({subagents:J.subagents});if(te?.ok===!1||te?.error)throw new Error(te.error??"Copilot rejected subagent settings");N(pe=>({...pe,subagents:J.subagents})),O({type:"info",text:\`Applied subagent policy \${J.label}: max \${J.subagents.maxConcurrency} concurrent, depth \${J.subagents.maxDepth}\`})}).catch(ee=>O({type:"error",text:\`Failed to apply subagent policy: \${y.errorFormattingFormatUnknown(ee)}\`}))}});}let se=re=>`;
    return replaceOnce(source, pickerEnd, policyUI, "native /subagents policy controls");
}

export async function activate({ registerAppSourceTransform }) {
    const path = process.env.AFTERBURNER_SUBAGENT_POLICY_CONFIG ??
        join(process.env.AFTERBURNER_HOME ?? join(process.env.USERPROFILE ?? "", ".afterburner"), "config", "subagent-policy.json");
    let input;
    try {
        input = JSON.parse(readFileSync(path, "utf8"));
    } catch (error) {
        if (error?.code !== "ENOENT") throw error;
        input = {
            version: 1,
            policies: {
                "three-workers": {
                    displayName: "Three Workers",
                    description: "Bound parallel delegation without forcing an unavailable model",
                    maxConcurrency: 3,
                    maxDepth: 1,
                    resultExposure: "status-and-final",
                    disabledSubagents: [],
                    agents: {}
                }
            }
        };
    }
    const config = loadPolicyConfig(input);
    registerAppSourceTransform(source => installNativeSubagentPolicy(source, config));
}
