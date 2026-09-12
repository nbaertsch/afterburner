import { CANONICAL_ID, CANONICAL_TITLE } from "./names.mjs";

export const menuActions = Object.freeze([
    { name: "solo", label: "Solo", key: "1", description: "Apply the Solo policy" },
    { name: "conservative", label: "Conservative", key: "2", description: "Apply the Conservative policy" },
    { name: "balanced", label: "Balanced", key: "3", description: "Apply the Balanced policy" },
    { name: "burst", label: "Burst", key: "4", description: "Apply the Burst policy" },
    { name: "reload", label: "Reload", key: "r", description: "Reload policy configuration" },
    { name: "clear", label: "Clear", key: "x", description: "Clear the session policy override" },
    { name: "close", label: "Close", key: "q", description: "Close menu" }
]);

export function modalFrame(snapshot = {}, detail = undefined) {
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

export function buildModalFrame(ui, snapshot = {}, detail = undefined) {
    const frame = modalFrame(snapshot, detail);
    if (!ui?.createUIDocument) return frame;
    const c = ui.components ?? ui;
    if (!c?.dialog || !c?.panel || !c?.button || !c?.badge || !c?.keyValue) return frame;
    const policies = Object.entries(snapshot.policies ?? {});
    const active = snapshot.activePolicy ?? "none";
    const actionBar = c.actionBar ?? c.toolbar;
    try {
        frame.document = ui.createUIDocument(
            c.dialog({ title: CANONICAL_TITLE, status: frame.status, modal: true }, [
                c.panel({ title: "Current session" }, [
                    c.row({}, [
                        c.badge({ label: `Active: ${active}`, tone: active === "none" ? "warning" : "success" }, [], { id: "sp-active-badge" }),
                        c.badge({ label: `Default: ${snapshot.defaultPolicy ?? "none"}`, tone: "info" }, [], { id: "sp-default-badge" })
                    ], { id: "sp-status-row" }),
                    c.keyValue({ items: [
                        { key: "Configuration", value: snapshot.configPath ?? "built-in defaults" },
                        { key: "Available policies", value: String(policies.length) }
                    ] }, [], { id: "sp-session-details" })
                ], { id: "sp-current-panel" }),
                c.panel({ title: "Choose a policy" }, [
                    c.list({
                        items: policies.map(([id, policy]) => ({
                            id,
                            label: policy.displayName,
                            description: `${policy.description} Concurrency ${policy.maxConcurrency}; depth ${policy.maxDepth}; exposure ${policy.resultExposure}.`,
                            selected: id === active
                        }))
                    }, [], { id: "sp-policy-list", accessibility: { role: "list", name: "Available subagent policies" } })
                ], { id: "sp-policy-panel" }),
                snapshot.error ? c.alert({ title: "Policy error", message: snapshot.error, tone: "danger" }, [], { id: "sp-error" }) : null,
                detail ? c.text({ value: detail, tone: "muted" }, [], { id: "sp-detail" }) : null,
                actionBar({ label: "Policy actions" }, menuActions.map(action =>
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
                c.help({ title: "Keyboard", text: "1 Solo · 2 Conservative · 3 Balanced · 4 Burst · r Reload · x Clear · q Close" }, [], { id: "sp-help" })
            ].filter(Boolean), { id: "sp-root", accessibility: { role: "dialog", name: CANONICAL_TITLE } }),
            { surfaceId: CANONICAL_ID, revision: Date.now(), locale: "en-US" }
        );
    } catch {}
    return frame;
}
