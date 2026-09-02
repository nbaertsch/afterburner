import { readFileSync } from "node:fs";
import { readFile } from "node:fs/promises";
import { join } from "node:path";

function replaceRequired(source, search, replacement, label) {
    const matches = source.split(search).length - 1;
    if (matches !== 1) {
        throw new Error(`Unable to install ${label}: expected one source match, found ${matches}.`);
    }
    return source.replace(search, replacement);
}

function installContextArrowControls(source) {
    source = replaceRequired(
        source,
        'W=(0,Kn.useRef)(r[0]??null),Y=(0,Kn.useRef)(null),{rows:Z,columns:de}=vi()',
        'W=(0,Kn.useRef)(r[0]??null),Y=(0,Kn.useRef)(null),[Qe,Je]=(0,Kn.useState)(!1),{rows:Z,columns:de}=vi()',
        "context-control focus state"
    );
    source = replaceRequired(
        source,
        'Ze=(0,Kn.useCallback)(Ue=>{let nt=W.current;if(!nt||!d)return;let ut=se(nt),ze=Ue<0?ut?.previousEffort:ut?.nextEffort;ze&&d(nt,ze)},[d,se]),qe=(0,Kn.useCallback)(()=>{let Ue=W.current;if(!Ue||!u)return;let nt=se(Ue);nt?.contextToggleable&&nt.nextContextTier&&u(Ue,nt.nextContextTier)},[u,se])',
        'Ze=(0,Kn.useCallback)(Ue=>{let nt=W.current;if(!nt)return;let ut=se(nt);if(Qe){let ze=Ue<0?ut?.previousContextTier:ut?.nextContextTier;ze&&u&&u(nt,ze);return}if(!d)return;let ze=Ue<0?ut?.previousEffort:ut?.nextEffort;ze&&d(nt,ze)},[d,u,se,Qe]),qe=(0,Kn.useCallback)(()=>{Ne&&$e&&Je(Ue=>!Ue)},[Ne,$e])',
        "context-control arrow routing"
    );
    source = replaceRequired(
        source,
        'on=$e?Azr(Ct,ut,{primary:Ht(R),muted:D,selected:Ht(T)}):null,ln=Ne?vzr(Ct,ut,{primary:Ht(R),muted:D}):null',
        'on=$e?Azr(Ct,ut&&!Qe,{primary:Ht(R),muted:D,selected:Ht(T)}):null,ln=Ne?vzr(Ct,ut&&Qe,{primary:Ht(R),muted:D,selected:Ht(T)}):null',
        "focused picker control rendering"
    );
    source = replaceRequired(
        source,
        '[j,se,Ge,q,T,P,N,R,D,$e,Ne,a])',
        '[j,se,Ge,q,T,P,N,R,D,$e,Ne,a,Qe])',
        "focused picker control render dependency"
    );
    source = replaceRequired(
        source,
        'function vzr(e,t,n){return t?Kn.default.createElement(Kn.default.Fragment,null,e.contextSegments.map(r=>Kn.default.createElement(Kn.default.Fragment,{key:r.key},Kn.default.createElement(b,{color:r.active?n.primary:n.muted},r.text)))):Kn.default.createElement(b,{color:n.muted},e.contextCellText)}',
        'function vzr(e,t,n){return e.contextToggleable?t?Kn.default.createElement(Kn.default.Fragment,null,Kn.default.createElement(b,{color:e.contextCanLower?n.selected:n.muted},"← "),Kn.default.createElement(b,{color:n.primary},e.contextCellText),Kn.default.createElement(b,{color:e.contextCanRaise?n.selected:n.muted}," →")):Kn.default.createElement(b,{color:n.muted},e.contextCellText):Kn.default.createElement(b,{color:n.muted},e.contextCellText)}',
        "context arrow renderer"
    );
    source = source.replaceAll(
        'onTab:Ne?qe:void 0,',
        'onTab:Ne&&$e?qe:void 0,'
    ).replaceAll(
        'additionalHints:{"left-right":$e&&"reasoning effort",tab:Ne&&"context window"',
        'additionalHints:{"left-right":Qe?"context window":$e&&"reasoning effort",tab:Ne&&$e&&"switch control"'
    );
    return source;
}

export async function activate({ pluginRoot, registerModelPickerAdapter, registerAppSourceTransform }) {
    const config = JSON.parse(
        await readFile(join(pluginRoot, "extensions", "byok-models", "models.json"), "utf8")
    );
    const contextWindowOptions = config.contextWindowOptions ?? [];
    const metadataPath = join(
        process.env.COPILOT_HOME ?? join(process.env.USERPROFILE ?? "", ".copilot"),
        "runtime-extension-data",
        "afterburner-byok-models",
        "model-metadata.json"
    );
    const upstreamBySelectionId = new Map(
        config.models.map((model) => [`${model.provider}/${model.id}`, model.modelId])
    );
    const runtimeMetadata = (selectionId) => {
        try {
            const metadata = JSON.parse(readFileSync(metadataPath, "utf8"));
            return metadata.models?.find((model) => model.selectionId === selectionId) ?? null;
        } catch {
            return null;
        }
    };

    registerModelPickerAdapter({
        matches: (selectionId) => upstreamBySelectionId.has(selectionId),
        upstreamModelId: (selectionId) => upstreamBySelectionId.get(selectionId),
        selectionIds: () => [...upstreamBySelectionId.keys()],
        supportedReasoningEfforts: (selectionId) => runtimeMetadata(selectionId)?.supportedReasoningEfforts,
        defaultReasoningEffort: (selectionId) => runtimeMetadata(selectionId)?.defaultReasoningEffort,
        maxContextWindowTokens: (selectionId) => runtimeMetadata(selectionId)?.maxContextWindowTokens,
        maxOutputTokens: (selectionId) => runtimeMetadata(selectionId)?.maxOutputTokens,
        contextWindowOptions: () => contextWindowOptions
    });
    registerAppSourceTransform(installContextArrowControls);
}
