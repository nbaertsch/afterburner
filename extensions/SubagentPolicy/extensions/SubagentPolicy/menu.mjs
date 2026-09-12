import { CANONICAL_ID, CANONICAL_TITLE } from "./names.mjs";

export const menuActions = Object.freeze([
    { name: "solo", label: "Solo", key: "1", description: "Apply the Solo policy" },
    { name: "conservative", label: "Conservative", key: "2", description: "Apply the Conservative policy" },
    { name: "balanced", label: "Balanced", key: "3", description: "Apply the Balanced policy" },
    { name: "burst", label: "Burst", key: "4", description: "Apply the Burst policy" },
    { name: "apply-selected-policy", label: "Apply", description: "Apply the selected policy" },
    { name: "reload", label: "Reload", key: "r", description: "Reload policy configuration" },
    { name: "clear", label: "Clear", key: "x", description: "Clear the session policy override" },
    { name: "close", label: "Close", key: "q", description: "Close menu" }
]);

function selectedPolicyID(snapshot, selectedPolicy) {
    const policies = Object.keys(snapshot.policies ?? {});
    if (selectedPolicy && policies.includes(selectedPolicy)) return selectedPolicy;
    if (snapshot.activePolicy && policies.includes(snapshot.activePolicy)) return snapshot.activePolicy;
    if (snapshot.defaultPolicy && policies.includes(snapshot.defaultPolicy)) return snapshot.defaultPolicy;
    return policies[0] ?? "";
}

export function modalFrame(snapshot = {}, detail = undefined, selectedPolicy = undefined) {
    const policies = Object.entries(snapshot.policies ?? {});
    const active = snapshot.activePolicy ?? "none";
    const frame = {
        id: CANONICAL_ID,
        title: CANONICAL_TITLE,
        status: `Active: ${active} · Policies: ${policies.length}${detail ? ` · ${detail}` : ""}`,
        body: [
            `Active policy: ${active}`,
            `Default policy: ${snapshot.defaultPolicy ?? "none"}`,
            `Config: ${snapshot.configPath ?? "built-in defaults"}`,
            "",
            ...policies.map(([id, policy]) =>
                `${id}: ${policy.displayName} — concurrency ${policy.maxConcurrency}, depth ${policy.maxDepth}, exposure ${policy.resultExposure}`
            ),
            snapshot.error ? `\nError: ${snapshot.error}` : undefined
        ].filter(value => value !== undefined).join("\n"),
        actions: menuActions
    };
    return frame;
}

export function buildModalFrame(ui, snapshot = {}, detail = undefined, selectedPolicy = undefined) {
    const selected = selectedPolicyID(snapshot, selectedPolicy);
    const frame = modalFrame(snapshot, detail, selected);
    if (!ui?.createUIDocument) return frame;
    const c = ui.components ?? ui;
    if (!c?.dialog || !c?.panel || !c?.button || !c?.badge || !c?.radioGroup) return frame;
    const policies = Object.entries(snapshot.policies ?? {});
    const active = snapshot.activePolicy ?? "none";
    const actionBar = c.actionBar ?? c.toolbar;
    try {
        frame.document = ui.createUIDocument(
            c.dialog({ title: CANONICAL_TITLE, status: frame.status, modal: true }, [
                c.row({}, [
                    c.badge({ label: active === "none" ? "No session policy" : `Active · ${active}`, tone: active === "none" ? "warning" : "success" }, [], { id: "sp-active-badge" }),
                    c.badge({ label: `Default · ${snapshot.defaultPolicy ?? "none"}`, tone: "info" }, [], { id: "sp-default-badge" })
                ], { id: "sp-status-row" }),
                c.panel({ title: "Policy" }, [
                    c.radioGroup({
                        label: "Select with ↑/↓, apply with Enter",
                        value: selected,
                        options: policies.map(([id, policy]) => ({
                            value: id,
                            label: `${policy.displayName}  ·  ${policy.maxConcurrency} agents  ·  depth ${policy.maxDepth}`,
                            description: policy.description
                        }))
                    }, [], {
                        id: "sp-policy-picker",
                        actionBindings: { change: "preview-policy", activate: "apply-selected-policy" },
                        accessibility: { role: "radiogroup", name: "Subagent policy" }
                    })
                ], { id: "sp-policy-panel" }),
                c.panel({ title: "Selected policy" }, [
                    c.markdown({ markdown: selectedPolicyMarkdown(snapshot, selected) }, [], { id: "sp-policy-detail" })
                ], { id: "sp-detail-panel" }),
                snapshot.error ? c.alert({ title: "Policy error", message: snapshot.error, tone: "danger" }, [], { id: "sp-error" }) : null,
                detail ? c.text({ value: detail, tone: "muted" }, [], { id: "sp-detail" }) : null,
                actionBar({ label: "Policy actions" }, menuActions.filter(action => !["solo", "conservative", "balanced", "burst"].includes(action.name)).map(action =>
                    c.button({
                        label: action.label,
                        actionId: action.name,
                        keybinding: action.key,
                        description: action.description,
                        variant: action.name === active ? "primary" : "secondary"
                    }, [], {
                        id: `sp-action-${action.name}`,
                        actionBindings: { activate: action.name }
                    })
                ), { id: "sp-action-bar", accessibility: { role: "toolbar", name: "Subagent policy actions" } }),
                c.help({ title: "Keyboard", text: "↑/↓ choose · Enter apply · r reload · x clear · q close · number shortcuts remain available" }, [], { id: "sp-help" })
            ].filter(Boolean), { id: "sp-root", accessibility: { role: "dialog", name: CANONICAL_TITLE } }),
            { surfaceId: CANONICAL_ID, revision: Date.now(), locale: "en-US" }
        );
    } catch {}
    return frame;
}

function selectedPolicyMarkdown(snapshot, id) {
    const policy = snapshot.policies?.[id];
    if (!policy) return "No policy selected.";
    const models = [...new Set(Object.values(policy.agents ?? {}).map(agent => agent.model).filter(Boolean))];
    const diagnostics = Object.entries(snapshot.diagnostics ?? {}).map(([agent, value]) =>
        `- ${agent}: configured **${value.configuredModel ?? "inherit"}**; resolved **${value.resolvedModel ?? "not observed"}**${value.verified ? " ✓" : " ⚠"}`
    );
    return [
        `**${policy.displayName}**`,
        "",
        policy.description,
        "",
        `- Maximum active subagents: **${policy.maxConcurrency}**`,
        `- Maximum nesting depth: **${policy.maxDepth}**`,
        `- Parent visibility: **${policy.resultExposure}**`,
        `- Required model routing: **${models.length ? models.join(", ") : "inherit parent model"}**`,
        ...(diagnostics.length ? ["", "**Runtime model diagnostics**", ...diagnostics] : [])
    ].join("\n");
}
