const component = kind => (props = {}, children = [], options = {}) => ({
    kind,
    props: props && typeof props === "object" ? props : {},
    children: Array.isArray(children) ? children.filter(Boolean) : [],
    ...options
});

export const components = new Proxy({}, {
    get: (_target, kind) => component(String(kind))
});

export function createUIDocument(root, options = {}) {
    return validateModalDocument({
        schemaVersion: 1,
        protocol: "afterburner.modal",
        revision: options.revision ?? 1,
        surfaceId: options.surfaceId ?? root?.props?.surfaceId,
        root,
        locale: options.locale,
        capabilities: options.capabilities
    });
}

export function validateModalDocument(document) {
    if (!document || typeof document !== "object" || Array.isArray(document)) {
        throw new Error("Modal document must be an object.");
    }
    if (!document.root || typeof document.root !== "object" || Array.isArray(document.root)) {
        throw new Error("Modal document requires a root component.");
    }
    if (typeof document.surfaceId !== "string" || !document.surfaceId) {
        throw new Error("Modal document requires a surfaceId.");
    }
    const revision = Number(document.revision);
    if (!Number.isInteger(revision) || revision < 1) {
        throw new Error("Modal document requires a positive revision.");
    }
    return {
        ...document,
        schemaVersion: 1,
        protocol: "afterburner.modal",
        revision
    };
}

export function modalFrameToDocument(canvas, frame, options = {}) {
    const title = frame.title ?? canvas.displayName ?? canvas.id;
    return createUIDocument(components.dialog({ title, modal: true }, [
        components.panel({ title }, [
            components.text({ value: frame.status ?? "" }),
            components.code({ language: "text", code: frame.body ?? "" })
        ])
    ], {
        id: `${canvas.id}-modal-root`,
        accessibility: { role: "dialog", name: title }
    }), {
        surfaceId: canvas.id,
        revision: options.revision ?? 1,
        locale: "en-US"
    });
}
