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
    try {
        return installContextArrowControlsUnsafe(source);
    } catch (error) {
        if (process.env.COPILOT_RUNTIME_EXTENSION_DEBUG === "1") {
            process.stderr.write(`[byo-models] context arrow controls unavailable: ${error?.message ?? String(error)}\n`);
        }
        return source;
    }
}

function installContextArrowControlsUnsafe(source) {
    if (source.includes('W=(0,Vn.useRef)(r[0]??null)')) {
        source = replaceRequired(
            source,
            'W=(0,Vn.useRef)(r[0]??null),Y=(0,Vn.useRef)(null),{rows:ee,columns:ce}=Ci()',
            'W=(0,Vn.useRef)(r[0]??null),Y=(0,Vn.useRef)(null),[Ke,Je]=(0,Vn.useState)(!1),{rows:ee,columns:ce}=Ci()',
            "context-control focus state"
        );
        source = replaceRequired(
            source,
            'Ve=(0,Vn.useCallback)(Ue=>{let nt=W.current;if(!nt||!d)return;let ut=se(nt),Ge=Ue<0?ut?.previousEffort:ut?.nextEffort;Ge&&d(nt,Ge)},[d,se]),qe=(0,Vn.useCallback)(()=>{let Ue=W.current;if(!Ue||!u)return;let nt=se(Ue);nt?.contextToggleable&&nt.nextContextTier&&u(Ue,nt.nextContextTier)},[u,se])',
            'Ve=(0,Vn.useCallback)(Ue=>{let nt=W.current;if(!nt)return;let ut=se(nt);if(Ke){let Ge=Ue<0?ut?.previousContextTier:ut?.nextContextTier;Ge&&u&&u(nt,Ge);return}if(!d)return;let Ge=Ue<0?ut?.previousEffort:ut?.nextEffort;Ge&&d(nt,Ge)},[d,u,se,Ke]),qe=(0,Vn.useCallback)(()=>{Ne&&Le&&Je(Ue=>!Ue)},[Ne,Le])',
            "context-control arrow routing"
        );
        source = replaceRequired(
            source,
            'ln=Le?Qzr(vt,ut,{primary:Gt(R),muted:O,selected:Gt(T)}):null,cn=Ne?Wzr(vt,ut,{primary:Gt(R),muted:O}):null',
            'ln=Le?Qzr(vt,ut&&!Ke,{primary:Gt(R),muted:O,selected:Gt(T)}):null,cn=Ne?Wzr(vt,ut&&Ke,{primary:Gt(R),muted:O,selected:Gt(T)}):null',
            "focused picker control rendering"
        );
        source = replaceRequired(
            source,
            '[j,se,$e,Q,T,P,N,R,O,Le,Ne,a])',
            '[j,se,$e,Q,T,P,N,R,O,Le,Ne,a,Ke])',
            "focused picker control render dependency"
        );
        source = replaceRequired(
            source,
            'function Wzr(e,t,n){return t?Vn.default.createElement(Vn.default.Fragment,null,e.contextSegments.map(r=>Vn.default.createElement(Vn.default.Fragment,{key:r.key},Vn.default.createElement(b,{color:r.active?n.primary:n.muted},r.text)))):Vn.default.createElement(b,{color:n.muted},e.contextCellText)}',
            'function Wzr(e,t,n){return e.contextToggleable?t?Vn.default.createElement(Vn.default.Fragment,null,Vn.default.createElement(b,{color:e.contextCanLower?n.selected:n.muted},"← "),Vn.default.createElement(b,{color:n.primary},e.contextCellText),Vn.default.createElement(b,{color:e.contextCanRaise?n.selected:n.muted}," →")):Vn.default.createElement(b,{color:n.muted},e.contextCellText):Vn.default.createElement(b,{color:n.muted},e.contextCellText)}',
            "context arrow renderer"
        );
        return source.replaceAll('onTab:Ne?qe:void 0,', 'onTab:Ne&&Le?qe:void 0,').replaceAll(
            'additionalHints:{"left-right":Le&&"reasoning effort",tab:Ne&&"context window"',
            'additionalHints:{"left-right":Ke?"context window":Le&&"reasoning effort",tab:Ne&&Le&&"switch control"'
        );
    }
    if (source.includes('j=(0,Wn.useRef)(r[0]??null)')) {
        source = replaceRequired(
            source,
            'j=(0,Wn.useRef)(r[0]??null),W=(0,Wn.useRef)(null),{rows:X,columns:ce}=Ti()',
            'j=(0,Wn.useRef)(r[0]??null),W=(0,Wn.useRef)(null),[$bbFocus,setBbFocus]=(0,Wn.useState)(!1),{rows:X,columns:ce}=Ti()',
            "context-control focus state"
        );
        source = replaceRequired(
            source,
            'dt=(0,Wn.useCallback)(Le=>{let tt=j.current;if(!tt||!d)return;let Ct=ae(tt),Ge=Le<0?Ct?.previousEffort:Ct?.nextEffort;Ge&&d(tt,Ge)},[d,ae]),ze=(0,Wn.useCallback)(()=>{let Le=j.current;if(!Le||!u)return;let tt=ae(Le);tt?.contextToggleable&&tt.nextContextTier&&u(Le,tt.nextContextTier)},[u,ae])',
            'dt=(0,Wn.useCallback)(Le=>{let tt=j.current;if(!tt)return;let Ct=ae(tt);if($bbFocus){let Ge=Le<0?Ct?.previousContextTier:Ct?.nextContextTier;Ge&&u&&u(tt,Ge);return}if(!d)return;let Ge=Le<0?Ct?.previousEffort:Ct?.nextEffort;Ge&&d(tt,Ge)},[d,u,ae,$bbFocus]),ze=(0,Wn.useCallback)(()=>{Be&&De&&setBbFocus(Le=>!Le)},[Be,De])',
            "context-control arrow routing"
        );
        source = replaceRequired(
            source,
            'jt=De?F6r(Tt,Ct,{primary:zt(R),muted:O,selected:zt(w)}):null,nn=Be?U6r(Tt,Ct,{primary:zt(R),muted:O}):null',
            'jt=De?F6r(Tt,Ct&&!$bbFocus,{primary:zt(R),muted:O,selected:zt(w)}):null,nn=Be?U6r(Tt,Ct&&$bbFocus,{primary:zt(R),muted:O,selected:zt(w)}):null',
            "focused picker control rendering"
        );
        source = replaceRequired(
            source,
            '[V,ae,$e,Q,w,P,M,R,O,De,Be,a])',
            '[V,ae,$e,Q,w,P,M,R,O,De,Be,a,$bbFocus])',
            "focused picker control render dependency"
        );
        source = replaceRequired(
            source,
            'function U6r(e,t,n){return t?Wn.default.createElement(Wn.default.Fragment,null,e.contextSegments.map(r=>Wn.default.createElement(Wn.default.Fragment,{key:r.key},Wn.default.createElement(b,{color:r.active?n.primary:n.muted},r.text)))):Wn.default.createElement(b,{color:n.muted},e.contextCellText)}',
            'function U6r(e,t,n){return e.contextToggleable?t?Wn.default.createElement(Wn.default.Fragment,null,Wn.default.createElement(b,{color:e.contextCanLower?n.selected:n.muted},"← "),Wn.default.createElement(b,{color:n.primary},e.contextCellText),Wn.default.createElement(b,{color:e.contextCanRaise?n.selected:n.muted}," →")):Wn.default.createElement(b,{color:n.muted},e.contextCellText):Wn.default.createElement(b,{color:n.muted},e.contextCellText)}',
            "context arrow renderer"
        );
        return source.replaceAll('onTab:Be?ze:void 0,', 'onTab:Be&&De?ze:void 0,').replaceAll(
            'additionalHints:{"left-right":De&&"reasoning effort",tab:Be&&"context window"',
            'additionalHints:{"left-right":$bbFocus?"context window":De&&"reasoning effort",tab:Be&&De&&"switch control"'
        );
    }
    if (source.includes('W=(0,Kn.useRef)(r[0]??null)')) {
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
        return source.replaceAll(
            'onTab:Ne?qe:void 0,',
            'onTab:Ne&&$e?qe:void 0,'
        ).replaceAll(
            'additionalHints:{"left-right":$e&&"reasoning effort",tab:Ne&&"context window"',
            'additionalHints:{"left-right":Qe?"context window":$e&&"reasoning effort",tab:Ne&&$e&&"switch control"'
        );
    }
    // No known minification profile matched; fail closed (leave source
    // unmodified) rather than blindly attempting a replace that would throw
    // and crash the entire Copilot app when Copilot's build changes its
    // variable-naming scheme again.
    return source;
}

export async function activate({ pluginRoot, registerModelPickerAdapter, registerAppSourceTransform, registerModelPickerRowDecorator }) {
    const configPath = process.env.AFTERBURNER_BYOMODELS_CONFIG ??
        join(process.env.AFTERBURNER_HOME ?? join(process.env.USERPROFILE ?? "", ".afterburner"),
            "config", "byomodels.json");
    const config = JSON.parse(
        await readFile(configPath, "utf8")
    );
    const contextWindowOptions = config.contextWindowOptions ?? [];
    const metadataPath = join(
        process.env.COPILOT_HOME ?? join(process.env.USERPROFILE ?? "", ".copilot"),
        "runtime-extension-data",
        "afterburner-byomodels",
        "model-metadata.json"
    );
    const upstreamBySelectionId = new Map(
        config.models.map((model) => [`${model.provider}/${model.id}`, model.modelId])
    );
    const badgeBySelectionId = new Map(
        config.models
            .filter((model) => typeof model.badge === "string" && model.badge.length > 0)
            .map((model) => [`${model.provider}/${model.id}`, model.badge])
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
    // registerModelPickerRowDecorator is only available on Afterburner builds
    // that support the model-picker.row-decorator.v1 mod point; older/newer
    // unprofiled Copilot builds simply won't offer per-model badges, which is
    // fine (fails closed at the framework level, not here).
    if (typeof registerModelPickerRowDecorator === "function" && badgeBySelectionId.size > 0) {
        registerModelPickerRowDecorator({
            id: "afterburner-byomodels-badge",
            render: (row) => badgeBySelectionId.get(row.selectionId) ?? null
        });
    }
}
