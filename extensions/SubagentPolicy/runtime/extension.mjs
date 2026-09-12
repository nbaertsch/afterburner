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
    const pickerAnchor = 'if(t)return RD.default.createElement(sdn,{builtInAgents:';
    const pickerReplacement = `if(t){let $abPolicies=${encoded},$abNative=RD.default.createElement(sdn,{builtInAgents:`;
    source = replaceOnce(source, pickerAnchor, pickerReplacement, "native /subagents picker");
    const pickerEnd = 'onCancel:()=>a(!1)});let se=re=>';
    const policyUI = `onCancel:()=>a(!1)});return RD.default.createElement(RD.default.Fragment,null,$abNative,RD.default.createElement(T,{flexDirection:"column",paddingX:1,marginTop:1},RD.default.createElement(b,{bold:!0},"Policy presets"),RD.default.createElement(b,{color:"gray"},"Apply routing, concurrency, and nesting limits in this session"),RD.default.createElement(Ml,{title:"Policy presets",items:$abPolicies.map(re=>({label:re.label,value:re.id,description:re.description})),escapeItem:{label:"Keep current settings",value:"cancel"},onConfirm:re=>{if(re==="cancel")return;let J=$abPolicies.find(ee=>ee.id===re);J&&(D.current=D.current.then(async()=>{await R.tools.updateSubagentSettings({settings:J.subagents}),N(ee=>({...ee,subagents:J.subagents})),O({type:"info",text:\`Applied subagent policy \${J.label}: max \${J.subagents.maxConcurrency} concurrent, depth \${J.subagents.maxDepth}\`})}).catch(ee=>O({type:"error",text:\`Failed to apply subagent policy: \${y.errorFormattingFormatUnknown(ee)}\`})))}})));}let se=re=>`;
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
