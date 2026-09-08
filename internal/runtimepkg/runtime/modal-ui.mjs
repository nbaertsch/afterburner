export const UI_PROTOCOL = "afterburner.ui";
export const UI_REVISION = 1;

const MAX_DOCUMENT_BYTES = 64 * 1024;
const MAX_NODES = 512;
const MAX_DEPTH = 16;
const MAX_CHILDREN = 64;
const idPattern = /^[a-z0-9][a-z0-9._-]{0,63}$/;
const escapePattern = /\x1b/;

export const componentKinds = Object.freeze([
    "dialog",
    "application",
    "surface",
    "window",
    "viewport",
    "stack",
    "row",
    "column",
    "group",
    "grid",
    "statusGrid",
    "split",
    "section",
    "box",
    "disclosure",
    "panel",
    "card",
    "scroll",
    "toolbar",
    "separator",
    "spacer",
    "breadcrumb",
    "contextMenu",
    "icon",
    "text",
    "markdown",
    "code",
    "keyValue",
    "detail",
    "badge",
    "alert",
    "toast",
    "progress",
    "meter",
    "bar",
    "sparkline",
    "spinner",
    "loading",
    "errorBoundary",
    "empty",
    "list",
    "table",
    "tree",
    "timeline",
    "tabs",
    "log",
    "commandPalette",
    "form",
    "button",
    "link",
    "textInput",
    "searchInput",
    "numberInput",
    "dateInput",
    "fileInput",
    "passwordInput",
    "textArea",
    "select",
    "checkbox",
    "radioGroup",
    "toggle",
    "slider",
    "actionBar",
    "keybindingHint",
    "pagination",
    "help",
    "confirmation",
    "prompt"
]);

const knownKinds = new Set(componentKinds);
const interactiveKinds = new Set([
    "button",
    "link",
    "textInput",
    "searchInput",
    "numberInput",
    "dateInput",
    "fileInput",
    "passwordInput",
    "textArea",
    "select",
    "checkbox",
    "radioGroup",
    "toggle",
    "slider",
    "tabs"
]);
const documentKeys = new Set(["schemaVersion", "protocol", "revision", "surfaceId", "root", "locale"]);
const nodeKeys = new Set(["id", "kind", "props", "children", "accessibility", "actionBindings", "metadata"]);

function plainObject(value) {
    if (value === null || typeof value !== "object" || Array.isArray(value)) return false;
    const prototype = Object.getPrototypeOf(value);
    return prototype === null || prototype === Object.prototype || prototype?.constructor?.name === "Object";
}

function validateJSONValue(value, path) {
    if (value === null || typeof value === "boolean" || typeof value === "number") return;
    if (typeof value === "string") {
        if (escapePattern.test(value)) throw new Error(`${path} may not contain terminal escape sequences.`);
        return;
    }
    if (Array.isArray(value)) {
        value.forEach((entry, index) => validateJSONValue(entry, `${path}[${index}]`));
        return;
    }
    if (!plainObject(value)) throw new Error(`${path} must contain plain JSON values.`);
    for (const [key, entry] of Object.entries(value)) validateJSONValue(entry, `${path}.${key}`);
}

function normalizeBuilderValue(value, path) {
    if (value === undefined) return undefined;
    if (value === null || typeof value === "boolean" || typeof value === "number" || typeof value === "string") {
        validateJSONValue(value, path);
        return value;
    }
    if (Array.isArray(value)) {
        return value.map((entry, index) => normalizeBuilderValue(entry, `${path}[${index}]`) ?? null);
    }
    if (!plainObject(value)) throw new Error(`${path} must contain plain JSON values.`);
    const normalized = {};
    for (const [key, entry] of Object.entries(value)) {
        const next = normalizeBuilderValue(entry, `${path}.${key}`);
        if (next !== undefined) normalized[key] = next;
    }
    return normalized;
}

function validateNode(node, state, depth, path) {
    if (!plainObject(node)) throw new Error(`${path} must be a component object.`);
    for (const key of Object.keys(node)) {
        if (!nodeKeys.has(key)) throw new Error(`${path}.${key} is not supported.`);
    }
    if (++state.nodes > MAX_NODES) throw new Error(`UI document exceeds the ${MAX_NODES}-node limit.`);
    if (depth > MAX_DEPTH) throw new Error(`UI document exceeds the ${MAX_DEPTH}-level depth limit.`);
    if (!knownKinds.has(node.kind)) throw new Error(`${path} has unsupported component kind '${node.kind}'.`);
    if (node.id !== undefined) {
        if (typeof node.id !== "string" || !idPattern.test(node.id)) throw new Error(`${path}.id is invalid.`);
        if (state.ids.has(node.id)) throw new Error(`UI document contains duplicate component id '${node.id}'.`);
        state.ids.add(node.id);
    } else if (interactiveKinds.has(node.kind)) {
        throw new Error(`${path} requires a stable id.`);
    }
    if (node.props !== undefined) {
        if (!plainObject(node.props)) throw new Error(`${path}.props must be an object.`);
        validateJSONValue(node.props, `${path}.props`);
    }
    if (node.accessibility !== undefined) {
        if (!plainObject(node.accessibility)) throw new Error(`${path}.accessibility must be an object.`);
        validateJSONValue(node.accessibility, `${path}.accessibility`);
    }
    if (node.actionBindings !== undefined) {
        if (!plainObject(node.actionBindings)) throw new Error(`${path}.actionBindings must be an object.`);
        validateJSONValue(node.actionBindings, `${path}.actionBindings`);
    }
    if (node.metadata !== undefined) {
        if (!plainObject(node.metadata)) throw new Error(`${path}.metadata must be an object.`);
        validateJSONValue(node.metadata, `${path}.metadata`);
    }
    const children = node.children ?? [];
    if (!Array.isArray(children)) throw new Error(`${path}.children must be an array.`);
    if (children.length > MAX_CHILDREN) throw new Error(`${path} exceeds the ${MAX_CHILDREN}-child limit.`);
    children.forEach((child, index) => validateNode(child, state, depth + 1, `${path}.children[${index}]`));
}

const component = kind => (props = {}, children = [], options = {}) => ({
    kind,
    props: plainObject(props) ? normalizeBuilderValue(props, `${kind}.props`) : {},
    children: Array.isArray(children) ? children.filter(Boolean) : [],
    ...(options.id !== undefined ? { id: options.id } : {}),
    ...(options.accessibility !== undefined ? { accessibility: normalizeBuilderValue(options.accessibility, `${kind}.accessibility`) } : {}),
    ...(options.actionBindings !== undefined ? { actionBindings: normalizeBuilderValue(options.actionBindings, `${kind}.actionBindings`) } : {}),
    ...(options.metadata !== undefined ? { metadata: normalizeBuilderValue(options.metadata, `${kind}.metadata`) } : {})
});

export const components = Object.freeze(Object.fromEntries(componentKinds.map(kind => [kind, component(kind)])));

export function createUIDocument(root, options = {}) {
    return validateModalDocument({
        schemaVersion: 1,
        protocol: UI_PROTOCOL,
        revision: options.revision ?? 1,
        surfaceId: options.surfaceId ?? root?.props?.surfaceId,
        root,
        ...(options.locale ? { locale: options.locale } : {})
    });
}

export const createDocument = createUIDocument;

export function validateModalDocument(document) {
    if (!plainObject(document)) throw new Error("UI document must be an object.");
    for (const key of Object.keys(document)) {
        if (!documentKeys.has(key)) throw new Error(`UI document field '${key}' is not supported.`);
    }
    if (document.schemaVersion !== undefined && document.schemaVersion !== 1) {
        throw new Error(`Unsupported UI document schema version '${document.schemaVersion}'.`);
    }
    if (document.protocol !== undefined && ![UI_PROTOCOL, "afterburner.modal"].includes(document.protocol)) {
        throw new Error(`Unsupported UI document protocol '${document.protocol}'.`);
    }
    if (!plainObject(document.root)) throw new Error("UI document requires a root component.");
    if (document.root.kind !== "dialog") throw new Error("UI document root must be a dialog.");
    if (typeof document.surfaceId !== "string" || !idPattern.test(document.surfaceId)) {
        throw new Error("UI document requires a valid surfaceId.");
    }
    const revision = Number(document.revision);
    if (!Number.isSafeInteger(revision) || revision < 1) {
        throw new Error("UI document requires a positive revision.");
    }
    if (document.locale !== undefined && (typeof document.locale !== "string" || document.locale.length > 64 || escapePattern.test(document.locale))) {
        throw new Error("UI document locale is invalid.");
    }
    validateNode(document.root, { nodes: 0, ids: new Set() }, 1, "root");
    const normalized = {
        ...document,
        schemaVersion: 1,
        protocol: UI_PROTOCOL,
        revision
    };
    if (Buffer.byteLength(JSON.stringify(normalized), "utf8") > MAX_DOCUMENT_BYTES) {
        throw new Error(`UI document exceeds the ${MAX_DOCUMENT_BYTES}-byte limit.`);
    }
    return normalized;
}

export const validateDocument = validateModalDocument;

export function modalFrameToDocument(canvas, frame, options = {}) {
    const title = frame.title ?? canvas.displayName ?? canvas.id;
    return createUIDocument(components.dialog({ title, modal: true }, [
        components.panel({ title }, [
            components.text({ value: frame.status ?? "" }),
            components.code({ language: "text", code: frame.body ?? "" })
        ])
    ], {
        id: "modal-root",
        accessibility: { role: "dialog", name: title }
    }), {
        surfaceId: canvas.id,
        revision: options.revision ?? 1,
        locale: "en-US"
    });
}
