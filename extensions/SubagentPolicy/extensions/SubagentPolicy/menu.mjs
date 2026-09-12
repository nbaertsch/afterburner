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
    return {
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
}
