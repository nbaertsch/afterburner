import { EventEmitter } from "node:events";
import { spawn } from "node:child_process";
import { createHash, randomUUID } from "node:crypto";

export const PROTOCOL = "afterburner.ui";
export const PROTOCOL_REVISION = 1;
export const COMPATIBILITY_ID = "afterburner.ui.r1";
export const SCHEMA_VERSION = 1;
export const DEFAULT_MAX_FRAME_BYTES = 1 << 20;
export const DEFAULT_MAX_JSON_DEPTH = 64;

export const componentKinds = Object.freeze([
  "application", "window", "surface", "viewport", "stack", "column", "row", "grid", "box", "section", "split", "scroll", "disclosure", "statusGrid", "panel", "card", "separator", "spacer", "empty", "text", "markdown", "code", "icon", "badge", "keyValue", "detail", "alert", "button", "link", "textInput", "passwordInput", "searchInput", "numberInput", "textArea", "select", "checkbox", "radioGroup", "toggle", "slider", "dateInput", "fileInput", "progress", "meter", "bar", "sparkline", "spinner", "loading", "list", "table", "tree", "timeline", "log", "form", "toolbar", "actionBar", "contextMenu", "tabs", "breadcrumb", "pagination", "help", "dialog", "toast", "errorBoundary", "confirmation", "prompt", "terminal", "canvas", "image", "video", "chart", "commandPalette", "keybindingHint", "extensionOutlet"
]);

const componentDescriptions = Object.freeze({
  "application": "Top-level application composition root.",
  "window": "Window-level container for host-managed UI.",
  "surface": "Mount point bound to a surface descriptor.",
  "viewport": "Scrollable viewport.",
  "stack": "One-dimensional vertical layout.",
  "column": "One-dimensional vertical layout column.",
  "row": "One-dimensional horizontal layout.",
  "grid": "Two-dimensional layout.",
  "box": "Generic boxed layout container.",
  "section": "Named section container.",
  "split": "Split-pane layout container.",
  "scroll": "Explicit scrollable region.",
  "disclosure": "Expandable/collapsible content region.",
  "statusGrid": "Dashboard-style status grid for health and metrics.",
  "panel": "Grouped content panel.",
  "card": "Elevated content region.",
  "separator": "Visual or semantic separator.",
  "spacer": "Intentional empty layout space.",
  "empty": "Purposeful empty-state message.",
  "text": "Plain text content.",
  "markdown": "Sanitized Markdown content.",
  "code": "Code block or inline code content.",
  "icon": "Decorative or semantic icon.",
  "badge": "Compact status label.",
  "keyValue": "Compact key/value facts and metadata.",
  "detail": "Detailed record inspection content.",
  "alert": "Prominent status, warning, or error callout.",
  "button": "User-invoked action control.",
  "link": "Navigation or external reference.",
  "textInput": "Single-line text input.",
  "passwordInput": "Secret text input.",
  "searchInput": "Search/filter input.",
  "numberInput": "Numeric input.",
  "textArea": "Multi-line text input.",
  "select": "Single or multi-select input.",
  "checkbox": "Boolean checkbox input.",
  "radioGroup": "Exclusive option group.",
  "toggle": "Binary switch control.",
  "slider": "Continuous or stepped numeric input.",
  "dateInput": "Date input.",
  "fileInput": "File path or file picker input.",
  "progress": "Progress indicator.",
  "meter": "Bounded scalar meter.",
  "bar": "Inline bar visualization.",
  "sparkline": "Compact inline trend visualization.",
  "spinner": "Indeterminate progress indicator.",
  "loading": "Loading state container.",
  "list": "Linear collection.",
  "table": "Tabular data collection.",
  "tree": "Hierarchical data collection.",
  "timeline": "Chronological event collection.",
  "log": "Streaming or historical log view.",
  "form": "Validated input group.",
  "toolbar": "Action strip.",
  "actionBar": "Primary command/action strip with keyboard affordances.",
  "contextMenu": "Contextual command menu.",
  "tabs": "Tabbed content switcher.",
  "breadcrumb": "Navigation path.",
  "pagination": "Paged collection navigation.",
  "help": "Contextual help content.",
  "dialog": "Modal or non-modal dialog.",
  "toast": "Transient notification.",
  "errorBoundary": "Recoverable render error boundary.",
  "confirmation": "Confirmation prompt.",
  "prompt": "User prompt or command prompt UI.",
  "terminal": "Terminal surface projection.",
  "canvas": "Extension-owned canvas.",
  "image": "Image content.",
  "video": "Video content.",
  "chart": "Data visualization.",
  "commandPalette": "Command discovery and invocation UI.",
  "keybindingHint": "Keyboard shortcut hint.",
  "extensionOutlet": "Policy-gated extension insertion point."
});

export const componentCatalog = deepFreeze(componentKinds.map((kind) => ({
  kind,
  stability: "stable",
  description: componentDescriptions[kind] ?? "Composable UI component."
})));

export const surfaceKinds = Object.freeze(["terminal", "modal", "panel", "inline", "statusLine", "commandPalette", "overlay"]);

const surfaceDescriptions = Object.freeze({
  "terminal": "Terminal-backed interactive surface.",
  "modal": "Host-managed modal surface.",
  "panel": "Persistent side-panel surface.",
  "inline": "Inline embedded surface.",
  "statusLine": "Compact status-line surface.",
  "commandPalette": "Command palette surface.",
  "overlay": "Overlay surface."
});

export const surfaceCatalog = deepFreeze(surfaceKinds.map((kind) => ({
  kind,
  stability: "stable",
  description: surfaceDescriptions[kind] ?? "Composable UI surface."
})));

export const capabilityKinds = Object.freeze([
  "ui.render.components", "ui.render.terminal", "ui.surface.terminal", "ui.surface.modal", "ui.surface.panel", "ui.surface.inline", "ui.surface.statusLine", "ui.surface.commandPalette", "ui.surface.overlay", "ui.action.invoke", "ui.data.read", "ui.data.write", "ui.stream.read", "ui.stream.write", "ui.theme.read", "ui.theme.write", "ui.localization.read", "ui.accessibility.inspect", "ui.policy.evaluate", "ui.audit.write", "ui.observability.sink", "ui.observability.black-box.sink"
]);

const capabilityDescriptions = Object.freeze({
  "ui.render.components": "Render versioned component trees and patches.",
  "ui.render.terminal": "Render terminal-backed component surfaces.",
  "ui.surface.terminal": "Create and manage terminal surfaces.",
  "ui.surface.modal": "Create and manage modal surfaces.",
  "ui.surface.panel": "Create and manage persistent panel surfaces.",
  "ui.surface.inline": "Create and manage inline embedded surfaces.",
  "ui.surface.statusLine": "Create and manage compact status-line surfaces.",
  "ui.surface.commandPalette": "Create and manage command-palette surfaces.",
  "ui.surface.overlay": "Create and manage overlay surfaces.",
  "ui.action.invoke": "Invoke declared UI actions.",
  "ui.data.read": "Read UI data sources.",
  "ui.data.write": "Mutate UI data sources.",
  "ui.stream.read": "Read UI stream frames.",
  "ui.stream.write": "Write UI stream frames.",
  "ui.theme.read": "Read semantic theme tokens.",
  "ui.theme.write": "Provide semantic theme tokens.",
  "ui.localization.read": "Read localized message bundles.",
  "ui.accessibility.inspect": "Inspect accessibility metadata.",
  "ui.policy.evaluate": "Evaluate UI grant policy decisions.",
  "ui.audit.write": "Write policy and lifecycle audit records.",
  "ui.observability.sink": "Receive optional UI observability events.",
  "ui.observability.black-box.sink": "Receive optional Black Box UI observability events."
});

export const capabilityCatalog = deepFreeze(capabilityKinds.map((id) => ({
  id,
  stability: "stable",
  description: capabilityDescriptions[id] ?? "UI capability."
})));
export const envelopeKinds = Object.freeze([
  "hello", "hello.result", "component.snapshot", "component.patch", "ui.event", "grant.policy", "lifecycle",
  "audit.event", "observation", "error", "ack", "backpressure"
]);
export const actionEffects = Object.freeze(["read", "write", "execute", "navigate", "dismiss"]);
export const dataSourceKinds = Object.freeze(["static", "query", "mutation", "subscription", "stream"]);
export const streamEncodings = Object.freeze(["utf8", "json", "vt", "bytes"]);
export const streamLifecycles = Object.freeze(["opening", "open", "draining", "closed", "errored", "backpressured"]);

const componentKindSet = new Set(componentKinds);
const surfaceKindSet = new Set(surfaceKinds);
const capabilityKindSet = new Set(capabilityKinds);
const envelopeKindSet = new Set(envelopeKinds);
const actionEffectSet = new Set(actionEffects);
const dataSourceKindSet = new Set(dataSourceKinds);
const streamEncodingSet = new Set(streamEncodings);
const streamLifecycleSet = new Set(streamLifecycles);
const stableIdPattern = /^[a-z0-9][a-z0-9._:-]{0,127}$/;
const actionIdPattern = /^[a-z][a-z0-9._:-]{0,127}$/;

export class UIContractError extends Error {
  constructor(code, message, details = {}) {
    super(message);
    this.name = "UIContractError";
    this.code = code;
    this.details = details;
    this.recoverable = details.recoverable === true;
  }
}

export class UITransportError extends Error {
  constructor(code, message, details = {}) {
    super(message);
    this.name = "UITransportError";
    this.code = code;
    this.details = details;
    this.recoverable = details.recoverable === true;
  }
}

function fail(code, message, details) {
  throw new UIContractError(code, message, details);
}

function assertObject(value, label) {
  if (!value || typeof value !== "object" || Array.isArray(value)) fail("ui.invalidEnvelope", `${label} must be an object.`);
  return value;
}

function validateStableId(id, label = "id", pattern = stableIdPattern) {
  if (typeof id !== "string" || !pattern.test(id)) fail("ui.invalidEnvelope", `${label} requires a stable lowercase id.`);
  return id;
}

function truncate(value, limit = 64 * 1024) {
  const text = value === undefined || value === null ? "" : String(value);
  return text.length <= limit ? text : `${text.slice(0, limit - 1)}…`;
}

function canonicalize(value) {
  if (value === undefined) return undefined;
  if (value === null || typeof value !== "object") return value;
  if (Array.isArray(value)) return value.map(canonicalize);
  const out = {};
  for (const key of Object.keys(value).sort()) {
    const entry = canonicalize(value[key]);
    if (entry !== undefined) out[key] = entry;
  }
  return out;
}

function stableHash(value) {
  return createHash("sha256").update(JSON.stringify(canonicalize(value))).digest("base64url").slice(0, 16).toLowerCase();
}

function deepClone(value) {
  if (value === undefined) return undefined;
  return JSON.parse(JSON.stringify(value));
}

function deepFreeze(value) {
  if (!value || typeof value !== "object" || Object.isFrozen(value)) return value;
  for (const entry of Object.values(value)) deepFreeze(entry);
  return Object.freeze(value);
}

function normalizeChildren(children) {
  if (children === undefined || children === null) return [];
  const list = Array.isArray(children) ? children : [children];
  return list.filter((child) => child !== undefined && child !== null).map((child) => {
    if (typeof child === "string" || typeof child === "number" || typeof child === "boolean") return text(String(child));
    return validateNode(child);
  });
}

function normalizeComponentArgs(kind, propsOrChildren, childrenOrOptions, maybeOptions) {
  let props = {};
  let children = [];
  let options = {};
  if ((kind === "text" || kind === "markdown" || kind === "code") && typeof propsOrChildren === "string") {
    props = kind === "text" ? { value: propsOrChildren } : kind === "markdown" ? { markdown: propsOrChildren } : { code: propsOrChildren };
    options = childrenOrOptions && typeof childrenOrOptions === "object" && !Array.isArray(childrenOrOptions) ? childrenOrOptions : {};
  } else if (Array.isArray(propsOrChildren) || isNodeLike(propsOrChildren)) {
    children = normalizeChildren(propsOrChildren);
    options = childrenOrOptions && typeof childrenOrOptions === "object" && !Array.isArray(childrenOrOptions) ? childrenOrOptions : {};
  } else {
    props = propsOrChildren && typeof propsOrChildren === "object" ? { ...propsOrChildren } : {};
    children = normalizeChildren(childrenOrOptions);
    options = maybeOptions && typeof maybeOptions === "object" ? maybeOptions : {};
  }
  return { props, children, options };
}

function isNodeLike(value) {
  return value && typeof value === "object" && typeof value.kind === "string" && typeof value.id === "string";
}

export function createNode(kind, propsOrChildren = {}, childrenOrOptions, maybeOptions) {
  if (!componentKindSet.has(kind)) fail("ui.unknownComponentKind", `Unknown component kind '${String(kind)}'.`);
  const { props, children, options } = normalizeComponentArgs(kind, propsOrChildren, childrenOrOptions, maybeOptions);
  const key = options.key ?? props.key;
  if (key !== undefined && typeof key !== "string") fail("ui.invalidComponentTree", `Component key for '${kind}' must be a string.`);
  delete props.key;
  const id = options.id ?? props.id ?? `${kind}-${stableHash({ kind, key, props, children: children.map((child) => ({ id: child.id, key: child.key, kind: child.kind })) })}`;
  delete props.id;
  validateStableId(id, `${kind} id`);
  const node = {
    id,
    kind,
    ...(key ? { key } : {}),
    ...(options.version !== undefined ? { version: Number(options.version) } : {}),
    ...(Object.keys(props).length ? { props: deepClone(props) } : {}),
    ...(children.length ? { children } : {}),
    ...(options.style ? { style: deepClone(options.style) } : {}),
    ...(options.accessibility ? { accessibility: deepClone(options.accessibility) } : {}),
    ...(options.localization ? { localization: deepClone(options.localization) } : {}),
    ...(options.dataBindings ? { dataBindings: [...options.dataBindings] } : {}),
    ...(options.actionBindings ? { actionBindings: { ...options.actionBindings } } : {}),
    ...(options.extensionSlots ? { extensionSlots: deepClone(options.extensionSlots) } : {}),
    ...(options.compatibility ? { compatibility: [...options.compatibility] } : {}),
    ...(options.metadata ? { metadata: deepClone(options.metadata) } : {})
  };
  return deepFreeze(validateNode(node));
}

export function validateNode(node, path = "/root") {
  assertObject(node, `component ${path}`);
  validateStableId(node.id, `component ${path} id`);
  if (!componentKindSet.has(node.kind)) fail("ui.unknownComponentKind", `Unknown component kind '${String(node.kind)}' at ${path}.`);
  if (node.key !== undefined && typeof node.key !== "string") fail("ui.invalidComponentTree", `Component key at ${path} must be a string.`);
  if (node.version !== undefined && (!Number.isSafeInteger(Number(node.version)) || Number(node.version) < 0)) {
    fail("ui.invalidComponentTree", `Component version at ${path} must be a non-negative integer.`);
  }
  if (node.children !== undefined) {
    if (!Array.isArray(node.children)) fail("ui.invalidComponentTree", `Component children at ${path} must be an array.`);
    const ids = new Set();
    for (let index = 0; index < node.children.length; index++) {
      const child = validateNode(node.children[index], `${path}/children/${index}`);
      if (ids.has(child.id)) fail("ui.invalidComponentTree", `Duplicate sibling id '${child.id}' at ${path}.`);
      ids.add(child.id);
    }
  }
  if (node.actionBindings !== undefined && (!node.actionBindings || typeof node.actionBindings !== "object" || Array.isArray(node.actionBindings))) {
    fail("ui.invalidComponentTree", `actionBindings at ${path} must be an object.`);
  }
  if (node.dataBindings !== undefined && !Array.isArray(node.dataBindings)) fail("ui.invalidComponentTree", `dataBindings at ${path} must be an array.`);
  return node;
}

export function createUIDocument(root, options = {}) {
  const surfaceId = options.surfaceId ?? root?.props?.surfaceId ?? "surface-default";
  validateStableId(surfaceId, "surfaceId");
  const tree = {
    root: validateNode(root),
    revision: Number.isSafeInteger(Number(options.revision)) ? Number(options.revision) : 1,
    surfaceId,
    ...(options.themeId ? { themeId: String(options.themeId) } : {}),
    ...(options.locale ? { locale: String(options.locale) } : {}),
    ...(options.capabilities ? { capabilities: [...options.capabilities] } : {})
  };
  return deepFreeze(validateUIDocument(tree));
}

export function validateUIDocument(tree) {
  assertObject(tree, "UIDocument");
  validateNode(tree.root);
  validateStableId(tree.surfaceId, "surfaceId");
  if (!Number.isSafeInteger(Number(tree.revision)) || Number(tree.revision) < 0) fail("ui.invalidComponentTree", "UIDocument revision must be a non-negative integer.");
  return tree;
}

const builderNames = {
  application: "application", window: "window", surface: "surface", viewport: "viewport", stack: "stack", column: "column", row: "row", grid: "grid", box: "box", section: "section", split: "split", scroll: "scroll", disclosure: "disclosure", statusGrid: "statusGrid", panel: "panel", card: "card", separator: "separator", spacer: "spacer", empty: "empty", text: "text", markdown: "markdown", code: "code", icon: "icon", badge: "badge", keyValue: "keyValue", detail: "detail", alert: "alert", button: "button", link: "link", textInput: "textInput", passwordInput: "passwordInput", searchInput: "searchInput", numberInput: "numberInput", textArea: "textArea", select: "select", checkbox: "checkbox", radioGroup: "radioGroup", toggle: "toggle", slider: "slider", dateInput: "dateInput", fileInput: "fileInput", progress: "progress", meter: "meter", bar: "bar", sparkline: "sparkline", spinner: "spinner", loading: "loading", list: "list", table: "table", tree: "tree", timeline: "timeline", log: "log", form: "form", toolbar: "toolbar", actionBar: "actionBar", contextMenu: "contextMenu", tabs: "tabs", breadcrumb: "breadcrumb", pagination: "pagination", help: "help", dialog: "dialog", toast: "toast", errorBoundary: "errorBoundary", confirmation: "confirmation", prompt: "prompt", terminal: "terminal", canvas: "canvas", image: "image", video: "video", chart: "chart", commandPalette: "commandPalette", keybindingHint: "keybindingHint", extensionOutlet: "extensionOutlet"
};

export const components = deepFreeze(Object.fromEntries(Object.entries(builderNames).map(([name, kind]) => [name, (...args) => createNode(kind, ...args)])));
export const application = components.application;
export const window = components.window;
export const surface = components.surface;
export const viewport = components.viewport;
export const stack = components.stack;
export const column = components.column;
export const row = components.row;
export const grid = components.grid;
export const box = components.box;
export const section = components.section;
export const split = components.split;
export const scroll = components.scroll;
export const disclosure = components.disclosure;
export const statusGrid = components.statusGrid;
export const panel = components.panel;
export const card = components.card;
export const separator = components.separator;
export const spacer = components.spacer;
export const empty = components.empty;
export const text = components.text;
export const markdown = components.markdown;
export const code = components.code;
export const icon = components.icon;
export const badge = components.badge;
export const keyValue = components.keyValue;
export const detail = components.detail;
export const alert = components.alert;
export const button = components.button;
export const link = components.link;
export const textInput = components.textInput;
export const passwordInput = components.passwordInput;
export const searchInput = components.searchInput;
export const numberInput = components.numberInput;
export const textArea = components.textArea;
export const select = components.select;
export const checkbox = components.checkbox;
export const radioGroup = components.radioGroup;
export const toggle = components.toggle;
export const slider = components.slider;
export const dateInput = components.dateInput;
export const fileInput = components.fileInput;
export const progress = components.progress;
export const meter = components.meter;
export const bar = components.bar;
export const sparkline = components.sparkline;
export const spinner = components.spinner;
export const loading = components.loading;
export const list = components.list;
export const table = components.table;
export const tree = components.tree;
export const timeline = components.timeline;
export const log = components.log;
export const form = components.form;
export const toolbar = components.toolbar;
export const actionBar = components.actionBar;
export const contextMenu = components.contextMenu;
export const tabs = components.tabs;
export const breadcrumb = components.breadcrumb;
export const pagination = components.pagination;
export const help = components.help;
export const dialog = components.dialog;
export const toast = components.toast;
export const errorBoundary = components.errorBoundary;
export const confirmation = components.confirmation;
export const prompt = components.prompt;
export const terminal = components.terminal;
export const canvas = components.canvas;
export const image = components.image;
export const video = components.video;
export const chart = components.chart;
export const commandPalette = components.commandPalette;
export const keybindingHint = components.keybindingHint;
export const extensionOutlet = components.extensionOutlet;

function defaultTimestamp() { return new Date().toISOString(); }
function messageId(prefix = "ui") { return `${prefix}-${randomUUID()}`; }

export function createHello(options = {}) {
  return {
    schemaVersion: SCHEMA_VERSION,
    protocol: PROTOCOL,
    compatibility: [COMPATIBILITY_ID],
    minimumRevision: 1,
    preferredRevision: PROTOCOL_REVISION,
    supportedRevisions: [PROTOCOL_REVISION],
    ...(options.sessionId ? { sessionId: options.sessionId } : {}),
    ...(options.extensionId ? { extensionId: options.extensionId } : {}),
    epoch: Number(options.epoch ?? 1),
    generation: Number(options.generation ?? 1),
    maxFrameBytes: Number(options.maxFrameBytes ?? DEFAULT_MAX_FRAME_BYTES),
    maxJsonDepth: Number(options.maxJsonDepth ?? DEFAULT_MAX_JSON_DEPTH),
    endpoint: options.endpoint ?? { name: "@afterburner/ui", capabilities: [] },
    sentAt: defaultTimestamp()
  };
}

export function negotiateHello(local, remote) {
  assertObject(local, "local hello");
  assertObject(remote, "remote hello");
  if (local.protocol !== PROTOCOL || remote.protocol !== PROTOCOL) return { accepted: false, error: structuredError("ui.transport.unsupportedRevision", "unsupported protocol", false) };
  const minimum = Math.max(Number(local.minimumRevision ?? 1), Number(remote.minimumRevision ?? 1));
  const localRevs = new Set((local.supportedRevisions ?? []).filter((value) => Number(value) >= minimum));
  const common = (remote.supportedRevisions ?? []).filter((value) => localRevs.has(value) && Number(value) >= minimum).sort((a, b) => b - a);
  if (common.length === 0) return { accepted: false, error: structuredError("ui.transport.unsupportedRevision", "no compatible protocol revision", false) };
  const revision = Number(common[0]);
  if ((local.rejectDowngrade && revision < local.preferredRevision) || (remote.rejectDowngrade && revision < remote.preferredRevision)) {
    return { accepted: false, error: structuredError("ui.transport.downgradeRejected", "protocol downgrade rejected", false) };
  }
  return {
    schemaVersion: SCHEMA_VERSION,
    protocol: PROTOCOL,
    compatibility: [COMPATIBILITY_ID],
    accepted: true,
    revision,
    minimumRevision: minimum,
    preferredRevision: Number(local.preferredRevision ?? PROTOCOL_REVISION),
    supportedRevisions: [...localRevs].sort((a, b) => a - b),
    sessionId: remote.sessionId ?? local.sessionId,
    extensionId: remote.extensionId ?? local.extensionId,
    epoch: Number(remote.epoch ?? local.epoch ?? 1),
    generation: Number(remote.generation ?? local.generation ?? 1),
    maxFrameBytes: Math.min(Number(local.maxFrameBytes || DEFAULT_MAX_FRAME_BYTES), Number(remote.maxFrameBytes || DEFAULT_MAX_FRAME_BYTES)),
    maxJsonDepth: Math.min(Number(local.maxJsonDepth || DEFAULT_MAX_JSON_DEPTH), Number(remote.maxJsonDepth || DEFAULT_MAX_JSON_DEPTH)),
    endpoint: local.endpoint ?? {}
  };
}

function structuredError(code, message, recoverable = false, details = {}) {
  return { code, message, recoverable, ...(Object.keys(details).length ? { details } : {}) };
}

export function createEnvelope(kind, payload = {}, options = {}) {
  if (!envelopeKindSet.has(kind)) fail("ui.invalidEnvelope", `Unsupported envelope kind '${String(kind)}'.`);
  const id = options.id ?? options.messageId ?? messageId(kind.replace(/[^a-z0-9]+/g, "-"));
  return {
    schemaVersion: SCHEMA_VERSION,
    protocol: PROTOCOL,
    revision: Number(options.revision ?? PROTOCOL_REVISION),
    ...(options.sessionId ? { sessionId: options.sessionId } : {}),
    ...(options.extensionId ? { extensionId: options.extensionId } : {}),
    messageId: id,
    id,
    ...(options.correlationId ? { correlationId: options.correlationId } : {}),
    ...(options.causationId ? { causationId: options.causationId } : {}),
    epoch: Number(options.epoch ?? 1),
    generation: Number(options.generation ?? 1),
    sequence: Number(options.sequence ?? 0),
    timestamp: options.timestamp ?? defaultTimestamp(),
    kind,
    source: options.source ?? { kind: "sdk", id: options.extensionId ?? "@afterburner/ui" },
    target: options.target ?? { kind: "renderer", id: "host" },
    compatibility: [COMPATIBILITY_ID],
    contentType: "application/json",
    payload: payload ?? {},
    ...(options.ack ? { ack: options.ack } : {}),
    ...(options.retry ? { retry: options.retry } : {}),
    ...(options.backpressure ? { backpressure: options.backpressure } : {}),
    ...(options.extensions ? { extensions: options.extensions } : {}),
    ...(options.idempotencyKey ? { idempotencyKey: options.idempotencyKey } : {})
  };
}

export function validateEnvelope(envelope) {
  assertObject(envelope, "envelope");
  if (envelope.schemaVersion !== SCHEMA_VERSION || envelope.protocol !== PROTOCOL || envelope.revision !== PROTOCOL_REVISION) {
    fail("ui.invalidEnvelope", "Envelope protocol revision is unsupported.");
  }
  if (!envelopeKindSet.has(envelope.kind)) fail("ui.invalidEnvelope", `Unsupported envelope kind '${String(envelope.kind)}'.`);
  if (!envelope.source || !envelope.target) fail("ui.invalidEnvelope", "Envelope requires source and target actors.");
  if (envelope.sequence !== undefined && (!Number.isSafeInteger(Number(envelope.sequence)) || Number(envelope.sequence) < 0)) fail("ui.invalidEnvelope", "Envelope sequence must be a non-negative integer.");
  return envelope;
}

export function encodeFrame(value, options = {}) {
  const maxFrameBytes = Number(options.maxFrameBytes ?? DEFAULT_MAX_FRAME_BYTES);
  const payload = Buffer.from(JSON.stringify(value), "utf8");
  if (payload.length === 0) throw new UITransportError("ui.transport.invalidFrame", "empty frames are not allowed");
  if (payload.length > maxFrameBytes) throw new UITransportError("ui.transport.frameTooLarge", "frame exceeds configured byte limit", { limitBytes: maxFrameBytes });
  assertJsonDepth(payload, Number(options.maxJsonDepth ?? DEFAULT_MAX_JSON_DEPTH));
  const frame = Buffer.allocUnsafe(4 + payload.length);
  frame.writeUInt32BE(payload.length, 0);
  payload.copy(frame, 4);
  return frame;
}

export function decodeFrame(buffer, options = {}) {
  const maxFrameBytes = Number(options.maxFrameBytes ?? DEFAULT_MAX_FRAME_BYTES);
  if (!Buffer.isBuffer(buffer)) buffer = Buffer.from(buffer);
  if (buffer.length < 4) throw new UITransportError("ui.transport.invalidFrame", "could not read frame length");
  const length = buffer.readUInt32BE(0);
  if (length === 0) throw new UITransportError("ui.transport.invalidFrame", "empty frames are not allowed");
  if (length > maxFrameBytes) throw new UITransportError("ui.transport.frameTooLarge", "frame exceeds configured byte limit", { limitBytes: maxFrameBytes });
  if (buffer.length < 4 + length) throw new UITransportError("ui.transport.invalidFrame", "could not read complete frame");
  const payload = buffer.subarray(4, 4 + length);
  assertJsonDepth(payload, Number(options.maxJsonDepth ?? DEFAULT_MAX_JSON_DEPTH));
  return JSON.parse(payload.toString("utf8"));
}

export class FrameTransport extends EventEmitter {
  constructor(stream, options = {}) {
    super();
    this.stream = stream;
    this.options = options;
    this.buffer = Buffer.alloc(0);
    stream.on?.("data", (chunk) => this._data(chunk));
    stream.on?.("error", (error) => this.emit("error", error));
    stream.on?.("close", () => this.emit("close"));
    stream.on?.("end", () => this.emit("close"));
  }
  write(value) {
    this.stream.write(encodeFrame(value, this.options));
  }
  close() {
    this.stream.end?.();
    this.stream.destroy?.();
  }
  _data(chunk) {
    this.buffer = Buffer.concat([this.buffer, Buffer.from(chunk)]);
    for (;;) {
      if (this.buffer.length < 4) return;
      const length = this.buffer.readUInt32BE(0);
      if (length > Number(this.options.maxFrameBytes ?? DEFAULT_MAX_FRAME_BYTES)) {
        this.emit("error", new UITransportError("ui.transport.frameTooLarge", "frame exceeds configured byte limit", { limitBytes: this.options.maxFrameBytes ?? DEFAULT_MAX_FRAME_BYTES }));
        this.close();
        return;
      }
      if (this.buffer.length < 4 + length) return;
      const frame = this.buffer.subarray(0, 4 + length);
      this.buffer = this.buffer.subarray(4 + length);
      try { this.emit("message", decodeFrame(frame, this.options)); }
      catch (error) { this.emit("error", error); }
    }
  }
}

export function createFrameTransport(stream, options = {}) { return new FrameTransport(stream, options); }

function assertJsonDepth(payload, maxDepth) {
  const text = Buffer.isBuffer(payload) ? payload.toString("utf8") : String(payload);
  let depth = 0;
  let inString = false;
  let escape = false;
  for (const char of text) {
    if (escape) { escape = false; continue; }
    if (char === "\\") { escape = inString; continue; }
    if (char === '"') { inString = !inString; continue; }
    if (inString) continue;
    if (char === "{" || char === "[") {
      depth++;
      if (depth > maxDepth) throw new UITransportError("ui.transport.jsonTooDeep", "frame JSON exceeds configured depth limit", { limitDepth: maxDepth });
    } else if (char === "}" || char === "]") depth--;
  }
}

function withAbort(promise, signal) {
  if (!signal) return promise;
  if (signal.aborted) return Promise.reject(abortError());
  return new Promise((resolve, reject) => {
    const onAbort = () => reject(abortError());
    signal.addEventListener("abort", onAbort, { once: true });
    Promise.resolve(promise).then(resolve, reject).finally(() => signal.removeEventListener("abort", onAbort));
  });
}

function abortError() {
  return new UITransportError("ui.transport.canceled", "operation canceled", { recoverable: true });
}

export class MemoryTransport extends EventEmitter {
  constructor(peer = null) {
    super();
    this.peer = peer;
    this.closed = false;
  }
  pair(peer) { this.peer = peer; peer.peer = this; return this; }
  write(value) {
    if (this.closed) throw new UITransportError("ui.transport.invalidFrame", "transport is closed");
    queueMicrotask(() => this.peer?.emit("message", deepClone(value)));
  }
  close() {
    if (this.closed) return;
    this.closed = true;
    const peer = this.peer;
    this.peer = null;
    this.emit("close");
    if (peer && !peer.closed) {
      peer.peer = null;
      peer.emit("close");
    }
  }
  static pair() {
    const left = new MemoryTransport();
    const right = new MemoryTransport(left);
    left.peer = right;
    return [left, right];
  }
}

export class ProtocolClient extends EventEmitter {
  constructor(options = {}) {
    super();
    this.options = { maxQueue: 128, maxFrameBytes: DEFAULT_MAX_FRAME_BYTES, maxJsonDepth: DEFAULT_MAX_JSON_DEPTH, ...options };
    this.transport = options.transport ?? null;
    this.createTransport = options.createTransport;
    this.endpoint = options.endpoint ?? { name: "@afterburner/ui" };
    this.sessionId = options.sessionId ?? "";
    this.extensionId = options.extensionId ?? "@afterburner/ui";
    this.epoch = Number(options.epoch ?? 1);
    this.generation = Number(options.generation ?? 1);
    this.sequence = 0;
    this.highWater = 0;
    this.pending = new Map();
    this.backpressure = false;
    this.connected = false;
    this.closed = false;
    this.queue = [];
    this._onMessage = (message) => void this._receive(message);
    this._onClose = () => this._handleClose();
  }

  async connect(options = {}) {
    if (options.transport) this.transport = options.transport;
    if (!this.transport && this.createTransport) this.transport = await this.createTransport();
    if (!this.transport) throw new UITransportError("ui.surfaceUnavailable", "UI transport is unavailable", { recoverable: true });
    this.transport.on?.("message", this._onMessage);
    this.transport.on?.("close", this._onClose);
    const hello = createHello({ endpoint: this.endpoint, sessionId: this.sessionId, extensionId: this.extensionId, epoch: this.epoch, generation: this.generation, maxFrameBytes: this.options.maxFrameBytes, maxJsonDepth: this.options.maxJsonDepth });
    this.transport.write?.(createEnvelope("hello", hello, this._envelopeOptions({ sequence: 0 })));
    this.connected = true;
    this.closed = false;
    this.emit("connected", { epoch: this.epoch, generation: this.generation });
    await this._flushQueue();
    return this;
  }

  async reconnect(options = {}) {
    this.transport?.off?.("message", this._onMessage);
    this.transport?.off?.("close", this._onClose);
    this.epoch = Number(options.epoch ?? this.epoch + 1);
    this.generation = Number(options.generation ?? this.generation + 1);
    this.connected = false;
    return this.connect(options);
  }

  close() {
    this.closed = true;
    this.connected = false;
    this.transport?.off?.("message", this._onMessage);
    this.transport?.off?.("close", this._onClose);
    this.transport?.close?.();
    for (const pending of this.pending.values()) pending.reject(new UITransportError("ui.transport.canceled", "transport closed", { recoverable: true }));
    this.pending.clear();
  }

  async send(kind, payload = {}, options = {}) {
    if (this.closed) throw new UITransportError("ui.transport.canceled", "client is closed", { recoverable: false });
    if (this.backpressure && options.allowDuringBackpressure !== true) {
      const err = new UITransportError("ui.transport.backpressure", "host backpressure is active", { recoverable: true });
      if (this.queue.length >= this.options.maxQueue) throw err;
      return withAbort(new Promise((resolve, reject) => this.queue.push({ kind, payload, options, resolve, reject })), options.signal);
    }
    const sequence = options.sequence ?? ++this.sequence;
    const envelope = createEnvelope(kind, payload, this._envelopeOptions({ ...options, sequence }));
    validateEnvelope(envelope);
    const sendPromise = new Promise((resolve, reject) => {
      if (options.expectAck === false) resolve({ ok: true, envelope });
      else this.pending.set(sequence, { resolve, reject, envelope, createdAt: Date.now() });
      try { this.transport.write?.(envelope); }
      catch (error) {
        this.pending.delete(sequence);
        reject(error);
      }
    });
    return withAbort(sendPromise, options.signal);
  }

  async snapshot(document, options = {}) { return this.send("component.snapshot", validateUIDocument(document), options); }
  async patch(patch, options = {}) { return this.send("component.patch", validatePatch(patch), options); }
  async event(event, options = {}) { return this.send("ui.event", event, options); }
  async lifecycle(payload, options = {}) { return this.send("lifecycle", payload, options); }
  async action(invocation, options = {}) { return this.send("ui.event", { type: "action.invoke", ...invocation }, options); }
  async data(sourceId, page, options = {}) { return this.send("observation", { type: "data.page", sourceId, page }, options); }
  async stream(frame, options = {}) {
    if (!streamLifecycleSet.has(frame.lifecycle ?? "open")) fail("ui.streamClosed", "invalid stream lifecycle");
    if (frame.encoding && !streamEncodingSet.has(frame.encoding)) fail("ui.invalidEnvelope", "invalid stream encoding");
    return this.send("observation", { type: "stream.frame", ...frame }, options);
  }

  _envelopeOptions(options = {}) {
    return { sessionId: this.sessionId, extensionId: this.extensionId, epoch: this.epoch, generation: this.generation, source: { kind: "sdk", id: this.extensionId }, target: { kind: "renderer", id: "host" }, ...options };
  }

  async _receive(envelope) {
    try { validateEnvelope(envelope); } catch (error) { this.emit("error", error); return; }
    if (Number(envelope.epoch ?? this.epoch) < this.epoch) {
      this.emit("stale", envelope);
      return;
    }
    if (Number(envelope.epoch ?? this.epoch) > this.epoch) {
      this.epoch = Number(envelope.epoch);
      this.emit("epoch", this.epoch);
    }
    if (envelope.kind === "hello.result") {
      const accepted = envelope.payload?.accepted !== false;
      if (!accepted) this.emit("error", new UITransportError(envelope.payload?.error?.code ?? "ui.transport.unsupportedRevision", envelope.payload?.error?.message ?? "hello rejected"));
      return;
    }
    if (envelope.kind === "ack") {
      const ack = envelope.ack ?? envelope.payload?.ack ?? envelope.payload;
      this._ack(ack);
      return;
    }
    if (envelope.kind === "backpressure") {
      const state = envelope.backpressure ?? envelope.payload;
      this.backpressure = state?.active === true;
      this.emit("backpressure", state);
      if (!this.backpressure) await this._flushQueue();
      return;
    }
    if (envelope.kind === "error") {
      const code = envelope.payload?.code ?? "ui.transport.invalidEnvelope";
      const targetSequence = Number(envelope.payload?.targetSequence ?? 0);
      const pending = this.pending.get(targetSequence);
      const err = new UITransportError(code, envelope.payload?.message ?? "UI transport error", { recoverable: envelope.payload?.recoverable === true });
      if (pending) { this.pending.delete(targetSequence); pending.reject(err); }
      else this.emit("error", err);
      return;
    }
    this.highWater = Math.max(this.highWater, Number(envelope.sequence ?? 0));
    this.emit(envelope.kind, envelope.payload, envelope);
    this.transport.write?.(createEnvelope("ack", {}, this._envelopeOptions({ sequence: ++this.sequence, ack: { epoch: this.epoch, highWater: this.highWater, contiguous: true } })));
  }

  _ack(ack = {}) {
    if (Number(ack.epoch ?? this.epoch) < this.epoch) return;
    const highWater = Number(ack.highWater ?? 0);
    for (const [sequence, pending] of [...this.pending.entries()]) {
      if (sequence <= highWater || (ack.received ?? []).includes(sequence)) {
        this.pending.delete(sequence);
        pending.resolve({ ok: true, ack });
      }
    }
  }

  _handleClose() {
    if (this.closed) return;
    this.connected = false;
    this.emit("disconnected");
  }

  async _flushQueue() {
    while (!this.backpressure && this.queue.length > 0) {
      const item = this.queue.shift();
      this.send(item.kind, item.payload, { ...item.options, allowDuringBackpressure: true }).then(item.resolve, item.reject);
    }
  }
}

export function createProtocolClient(options = {}) { return new ProtocolClient(options); }

function suggestionSuffix(value, candidates) {
  const candidate = closestCatalogValue(String(value), candidates);
  return candidate ? ` Did you mean '${candidate}'?` : "";
}

function closestCatalogValue(value, candidates) {
  if (!value || candidates.length === 0) return "";
  let best = "";
  let bestDistance = value.length + 1;
  for (const candidate of candidates) {
    const distance = levenshteinDistance(value, candidate);
    if (distance < bestDistance || (distance === bestDistance && candidate < best)) {
      best = candidate;
      bestDistance = distance;
    }
  }
  const limit = Math.max(2, Math.floor(value.length / 3));
  return bestDistance <= limit ? best : "";
}

function levenshteinDistance(a, b) {
  let previous = Array.from({ length: b.length + 1 }, (_, index) => index);
  let current = new Array(b.length + 1).fill(0);
  for (let i = 1; i <= a.length; i++) {
    current[0] = i;
    for (let j = 1; j <= b.length; j++) {
      const cost = a[i - 1] === b[j - 1] ? 0 : 1;
      current[j] = Math.min(previous[j] + 1, current[j - 1] + 1, previous[j - 1] + cost);
    }
    [current, previous] = [previous, current];
  }
  return previous[b.length];
}

function validateCapability(value, label = "UI") {
  if (typeof value !== "string" || !capabilityKindSet.has(value)) fail("ui.invalidEnvelope", `Unknown ${label} capability '${String(value)}'.${suggestionSuffix(value, capabilityKinds)}`);
  return value;
}

function validateCapabilityList(values, label) {
  for (const capability of values ?? []) validateCapability(capability, label);
}

export function validateSurfaceDescriptor(descriptor) {
  assertObject(descriptor, "surface descriptor");
  validateStableId(descriptor.id, "surface id");
  if (!surfaceKindSet.has(descriptor.kind)) fail("ui.invalidEnvelope", `Unknown surface kind '${String(descriptor.kind)}'.${suggestionSuffix(descriptor.kind, surfaceKinds)}`);
  for (const kind of descriptor.supportedComponents ?? []) {
    if (!componentKindSet.has(kind)) fail("ui.unknownComponentKind", `Unknown supported component kind '${String(kind)}'.${suggestionSuffix(kind, componentKinds)}`);
  }
  validateCapabilityList(descriptor.requiredCapabilities, "required");
  for (const action of descriptor.actions ?? []) validateActionDescriptor(action);
  for (const source of descriptor.dataSources ?? []) validateDataSourceDescriptor(source);
  for (const stream of descriptor.streams ?? []) validateStreamDescriptor(stream);
  return descriptor;
}

function validateActionDescriptor(action) {
  assertObject(action, "action descriptor");
  validateStableId(action.id, "action id", actionIdPattern);
  if (typeof action.title !== "string" || action.title.length === 0) fail("ui.invalidEnvelope", "Action descriptor requires title.");
  if (!actionEffectSet.has(action.effect)) fail("ui.invalidEnvelope", "Action descriptor has invalid effect.");
}

function validateDataSourceDescriptor(source) {
  assertObject(source, "data source descriptor");
  validateStableId(source.id, "data source id");
  if (!dataSourceKindSet.has(source.kind)) fail("ui.invalidEnvelope", "Data source descriptor has invalid kind.");
}

function validateStreamDescriptor(stream) {
  assertObject(stream, "stream descriptor");
  validateStableId(stream.id, "stream id");
  if (!streamEncodingSet.has(stream.encoding)) fail("ui.invalidEnvelope", "Stream descriptor has invalid encoding.");
}


function normalizeExtensionId(value) {
  if (value === undefined || value === null || value === "") return undefined;
  const text = String(value);
  if (!/^[a-z0-9][a-z0-9._:-]{0,127}$/.test(text)) fail("ui.invalidEnvelope", "ownerExtensionId requires a stable lowercase id.");
  return text;
}

function authorizationDenied(message, details = {}) {
  throw new UIContractError("ui.authorizationDenied", message, { recoverable: false, ...details });
}

function asStringSet(values) {
  return new Set((Array.isArray(values) ? values : []).filter((value) => typeof value === "string"));
}

function manifestUiCapabilities(manifest) { return asStringSet(manifest?.ui?.capabilities); }
function manifestLegacyCapabilities(manifest) { return asStringSet(manifest?.capabilities); }

function manifestSurfaceDeclaration(manifest, surfaceId) {
  return (manifest?.ui?.surfaces ?? []).find((surface) => surface?.id === surfaceId) ?? null;
}

function resourceMatches(pattern, resource) {
  if (pattern === "*" || pattern === resource) return true;
  if (typeof pattern === "string" && pattern.endsWith("/*") && typeof resource === "string") return resource.startsWith(pattern.slice(0, -1));
  return false;
}

function grantCapabilityMatches(grant, capability) {
  const capabilities = asStringSet(grant?.capabilities);
  return capabilities.has(capability);
}

function grantResourceMatches(grant, resource, options = {}) {
  if (options.matchAnyResource) return true;
  const resources = Array.isArray(grant?.resources) && grant.resources.length ? grant.resources : ["*"];
  return resources.some((entry) => resourceMatches(entry, resource));
}

function manifestGrantDecision(manifest, capability, resource, options = {}) {
  const policy = manifest?.ui?.grantPolicy;
  const grants = Array.isArray(policy?.grants) ? policy.grants : [];
  const matching = (grant) => grantCapabilityMatches(grant, capability) && grantResourceMatches(grant, resource, options);
  const deny = grants.find((grant) => grant?.effect === "deny" && matching(grant));
  if (deny) return { allowed: false, reason: "grant-denied", grantId: deny.id };
  const allow = grants.find((grant) => grant?.effect === "allow" && matching(grant));
  if (allow) return { allowed: true, grantId: allow.id };
  if (!policy) return null;
  return policy.denyByDefault === false ? { allowed: true, reason: "default-allow" } : { allowed: false, reason: "grant-denied" };
}

function requiredSurfaceCapabilities(descriptor, manifest) {
  const required = new Set(["ui.render.components", "ui.surface." + descriptor.kind]);
  for (const capability of descriptor.requiredCapabilities ?? []) required.add(capability);
  const declared = manifestSurfaceDeclaration(manifest, descriptor.id);
  for (const capability of declared?.requiredCapabilities ?? []) required.add(capability);
  if ((descriptor.actions ?? []).length > 0) required.add("ui.action.invoke");
  if ((descriptor.dataSources ?? []).length > 0) required.add("ui.data.read");
  if ((descriptor.streams ?? []).length > 0) required.add("ui.stream.read");
  return [...required];
}

function normalizeGrantDecision(decision) {
  if (decision === undefined || decision === null) return null;
  if (decision === true || decision === "allow") return { allowed: true };
  if (decision === false || decision === "deny") return { allowed: false, reason: "grant-denied" };
  if (typeof decision === "object") {
    if (decision.allowed === true || decision.granted === true || decision.effect === "allow") return { allowed: true };
    if (decision.allowed === false || decision.granted === false || decision.effect === "deny") {
      return { allowed: false, reason: decision.reason ?? decision.code ?? "grant-denied" };
    }
  }
  return null;
}

function manifestAuthorizes(request, context = {}) {
  const manifest = context.manifest;
  const ownerExtensionId = context.ownerExtensionId;
  if (manifest?.id && ownerExtensionId && manifest.id !== ownerExtensionId) {
    return { allowed: false, reason: "manifest-owner-mismatch" };
  }
  if (ownerExtensionId && request.ownerExtensionId && request.ownerExtensionId !== ownerExtensionId) {
    return { allowed: false, reason: "owner-mismatch" };
  }
  if (request.operation === "modalCanvas" && manifestLegacyCapabilities(manifest).has("modal-canvas")) {
    return { allowed: true };
  }
  if (request.operation === "runtimeObserver" && manifestLegacyCapabilities(manifest).has("runtime-observer")) {
    return { allowed: true };
  }
  const capabilities = Array.isArray(request.capabilities) ? request.capabilities : (request.capability ? [request.capability] : []);
  if (capabilities.length === 0) return { allowed: true };
  const declared = manifestUiCapabilities(manifest);
  if (manifest && capabilities.some((capability) => !declared.has(capability))) {
    return { allowed: false, reason: "capability-not-declared" };
  }
  if (!manifest && context.denyByDefault === true) return { allowed: false, reason: "capability-not-declared" };
  const resource = request.resource ?? request.surfaceId ?? "*";
  const matchAnyResource = request.operation === "hasCapability" && request.resource === undefined && request.surfaceId === undefined;
  const grantDecisions = capabilities.map((capability) => manifestGrantDecision(manifest, capability, resource, { matchAnyResource })).filter(Boolean);
  const denied = grantDecisions.find((decision) => decision.allowed === false);
  if (denied) return denied;
  if (grantDecisions.length === capabilities.length && grantDecisions.every((decision) => decision.allowed === true)) return { allowed: true };
  if (!manifest?.ui?.grantPolicy && context.denyByDefault !== true) return { allowed: true };
  if (manifest?.ui?.grantPolicy?.denyByDefault === false) return { allowed: true };
  return { allowed: false, reason: "grant-denied" };
}

function createGrantAuthorizer(options = {}) {
  const resolver = options.grantResolver ?? options.authorize ?? null;
  const context = { ownerExtensionId: normalizeExtensionId(options.ownerExtensionId), manifest: options.manifest, denyByDefault: options.denyByDefault === true };
  return (request) => {
    const manifestDecision = manifestAuthorizes(request, context);
    if (typeof resolver === "function") {
      const decision = normalizeGrantDecision(resolver({ ...request, ...context }));
      if (decision?.allowed === false) return decision;
      if (decision?.allowed === true) return manifestDecision.allowed ? decision : manifestDecision;
    }
    return manifestDecision;
  };
}

function observationId(value) {
  validateStableId(value, "observability sink id");
  return value;
}

function validateObservabilitySinkDescriptor(descriptor) {
  assertObject(descriptor, "observability sink descriptor");
  observationId(descriptor.id);
  if (descriptor.extensionId !== undefined) normalizeExtensionId(descriptor.extensionId);
  if (descriptor.capability !== undefined) validateCapability(descriptor.capability, "observability");
  return descriptor;
}

const safeObservationAttributeKeys = new Set([
  "model", "previousModel", "newModel", "toolName", "extensionId", "extensionKind", "observerId", "providerId",
  "agentId", "taskId", "state", "phase", "delivery", "hookType", "mode", "previousMode", "changeSource",
  "contextTier", "reasoningEffort", "previousReasoningEffort", "shutdownType", "errorType", "warningType", "infoType",
  "initiator", "transport", "success", "statusCode", "turn", "toolCount", "eventCount", "durationMs", "ttftMs",
  "outputTtftMs", "interTokenLatencyMs", "contextWindowTokens", "maxContextWindowTokens", "maxOutputTokens",
  "queueDepth", "delivered", "dropped", "failures", "disposeFailures", "eventTypeCount", "taskCount", "revision",
  "surfaceId", "instanceId", "hostId", "sinkId", "envelopeId", "envelopeKind", "uiEventType", "lifecycleState",
  "recoveryState", "securityDecision", "policyDecision", "grantId", "reason", "kind", "operation", "fallback", "actionId"
]);
const deniedObservationKeyPattern = /(prompt|content|message|summary|result|arguments|input|output|response|body|path|cwd|directory|file|secret|token|key|credential|password|document|payload|source|patch)/i;

function sanitizeObservationAttributeValue(value, depth = 0) {
  if (value === null || typeof value === "boolean") return value;
  if (typeof value === "number") return Number.isFinite(value) ? value : undefined;
  if (typeof value === "string") {
    const printable = value.replace(/[\u0000-\u001f\u007f]/g, " ").trim();
    return printable.length <= 192 ? printable : printable.slice(0, 191) + "…";
  }
  if (depth >= 2) return undefined;
  if (Array.isArray(value)) return value.slice(0, 16).map((entry) => sanitizeObservationAttributeValue(entry, depth + 1)).filter((entry) => entry !== undefined);
  if (value && typeof value === "object" && Object.getPrototypeOf(value) === Object.prototype) {
    const output = {};
    for (const [key, entry] of Object.entries(value).slice(0, 16)) {
      if (!safeObservationAttributeKeys.has(key) || deniedObservationKeyPattern.test(key)) continue;
      const sanitized = sanitizeObservationAttributeValue(entry, depth + 1);
      if (sanitized !== undefined) output[key] = sanitized;
    }
    return output;
  }
  return undefined;
}

function metadataOnlyObservation(observation = {}) {
  const attributes = {};
  for (const [key, value] of Object.entries(observation.attributes ?? {})) {
    if (!safeObservationAttributeKeys.has(key) || deniedObservationKeyPattern.test(key)) continue;
    const sanitized = sanitizeObservationAttributeValue(value);
    if (sanitized !== undefined) attributes[key] = sanitized;
  }
  for (const key of ["surfaceId", "sinkId", "envelopeId", "envelopeKind", "state", "reason", "operation", "fallback"]) {
    if (observation[key] === undefined || attributes[key] !== undefined) continue;
    const sanitized = sanitizeObservationAttributeValue(observation[key]);
    if (sanitized !== undefined) attributes[key] = sanitized;
  }
  return deepFreeze({
    schemaVersion: SCHEMA_VERSION,
    protocol: PROTOCOL,
    revision: PROTOCOL_REVISION,
    type: typeof observation.type === "string" ? observation.type : "ui.host.lifecycle",
    at: defaultTimestamp(),
    ...(attributes.surfaceId ? { surfaceId: attributes.surfaceId } : {}),
    ...(attributes.sinkId ? { sinkId: attributes.sinkId } : {}),
    ...(attributes.envelopeKind ? { envelopeKind: attributes.envelopeKind } : {}),
    attributes
  });
}

export function defineSurface(definition) {
  return defaultRuntime.defineSurface(definition);
}
export function registerSurface(definition) { return defaultRuntime.registerSurface(definition); }
export function open(id, input, options) { return defaultRuntime.open(id, input, options); }
export function renderSurface(id, document, options) { return defaultRuntime.renderSurface(id, document, options); }
export function update(id, next, options) { return defaultRuntime.update(id, next, options); }
export function patch(id, patchDoc, options) { return defaultRuntime.patch(id, patchDoc, options); }
export function patchSurface(id, patchDoc, options) { return defaultRuntime.patchSurface(id, patchDoc, options); }
export function close(id, options) { return defaultRuntime.close(id, options); }
export function closeSurface(id, options) { return defaultRuntime.closeSurface(id, options); }
export function invoke(id, actionId, parameters, options) { return defaultRuntime.invoke(id, actionId, parameters, options); }
export function subscribe(id, listener) { return defaultRuntime.subscribe(id, listener); }
export function fallback(id) { return defaultRuntime.fallback(id); }
export function registerObservabilitySink(sink, listener) { return defaultRuntime.registerObservabilitySink(sink, listener); }
export function subscribeObservability(descriptor, listener) { return defaultRuntime.subscribeObservability(descriptor, listener); }
export function diagnostics() { return defaultRuntime.diagnostics(); }

export class UIRuntime {
  constructor(options = {}) {
    this.bridge = options.bridge ?? null;
    this.client = options.client ?? null;
    this.ownerExtensionId = normalizeExtensionId(options.ownerExtensionId);
    this.manifest = options.manifest;
    this.authorize = options.authorize === false ? null : createGrantAuthorizer(options);
    this.enforceAuthorization = Boolean(options.manifest || options.grantResolver || options.authorize || options.denyByDefault === true);
    this.surfaces = new Map();
    this.instances = new Map();
    this.fallbacks = new Map();
    this.subscribers = new Map();
    this.observabilitySinks = new Map();
    this.sequence = 0;
    this.counters = { registered: 0, opened: 0, updated: 0, patched: 0, closed: 0, invoked: 0, fallbackCount: 0, errors: 0, sidecars: 0, observabilitySinks: 0, observations: 0 };
  }

  defineSurface(definition) {
    assertObject(definition, "surface definition");
    const requestedOwner = normalizeExtensionId(definition.ownerExtensionId ?? this.ownerExtensionId);
    this._assertCallerOwner(requestedOwner, definition.ownerExtensionId);
    const descriptor = validateSurfaceDescriptor({
      id: definition.id,
      kind: definition.kind ?? "panel",
      title: definition.title ?? definition.displayName ?? definition.id,
      lifecycle: "declared",
      ...(requestedOwner ? { ownerExtensionId: requestedOwner } : {}),
      supportedComponents: definition.supportedComponents ?? componentKinds,
      requiredCapabilities: definition.requiredCapabilities,
      actions: (definition.actions ?? []).map((action) => ({ id: action.id ?? action.name, title: action.title ?? action.label ?? action.id ?? action.name, description: action.description, effect: action.effect ?? "execute", inputSchema: action.inputSchema, outputSchema: action.outputSchema, confirmation: action.confirmation })),
      dataSources: definition.dataSources ?? [],
      streams: definition.streams ?? [],
      createdAt: defaultTimestamp(),
      metadata: definition.metadata
    });
    const capabilities = requiredSurfaceCapabilities(descriptor, this.manifest);
    this._authorize({ operation: "registerSurface", ownerExtensionId: requestedOwner, surfaceId: descriptor.id, resource: descriptor.id, capabilities, descriptor });
    const existing = this.surfaces.get(descriptor.id);
    if (existing) {
      this._assertSurfaceOwner(existing, requestedOwner, "registerSurface");
      fail("ui.invalidEnvelope", "Surface '" + descriptor.id + "' is already defined.");
    }
    const declared = manifestSurfaceDeclaration(this.manifest, descriptor.id);
    if (this.enforceAuthorization && this.manifest?.ui?.surfaces?.length && !declared) {
      authorizationDenied("Surface '" + descriptor.id + "' is not declared by the extension manifest.", { surfaceId: descriptor.id, ownerExtensionId: requestedOwner });
    }
    if (declared?.kind && declared.kind !== descriptor.kind) {
      authorizationDenied("Surface '" + descriptor.id + "' kind is not authorized by the extension manifest.", { surfaceId: descriptor.id, ownerExtensionId: requestedOwner });
    }
    const surface = { descriptor, render: definition.render, onOpen: definition.open, onClose: definition.close, actions: definition.actions ?? [], dataSources: definition.dataSources ?? [], streams: definition.streams ?? [] };
    this.surfaces.set(descriptor.id, surface);
    this.counters.registered = this.surfaces.size;
    const runtime = this;
    const ownerOptions = requestedOwner ? { ownerExtensionId: requestedOwner } : {};
    return deepFreeze({
      id: descriptor.id,
      descriptor: deepClone(descriptor),
      open: (input, options = {}) => runtime.open(descriptor.id, input, { ...options, ...ownerOptions }),
      update: (next, options = {}) => runtime.update(descriptor.id, next, { ...options, ...ownerOptions }),
      patch: (patchDoc, options = {}) => runtime.patch(descriptor.id, patchDoc, { ...options, ...ownerOptions }),
      close: (options = {}) => runtime.close(descriptor.id, { ...options, ...ownerOptions }),
      invoke: (actionId, parameters, options = {}) => runtime.invoke(descriptor.id, actionId, parameters, { ...options, ...ownerOptions }),
      subscribe: (listener, options = {}) => runtime.subscribe(descriptor.id, listener, { ...options, ...ownerOptions }),
      fallback: (options = {}) => runtime.fallback(descriptor.id, { ...options, ...ownerOptions }),
      diagnostics: () => runtime.diagnostics({ ...ownerOptions })
    });
  }

  registerSurface(definition) { return this.defineSurface(definition); }

  async open(id, input = {}, options = {}) {
    const surface = this._surface(id, options);
    this._authorizeSurfaceOperation(surface, "open", options, ["ui.render.components", "ui.surface." + surface.descriptor.kind]);
    const state = typeof surface.onOpen === "function" ? await surface.onOpen(input) : input;
    const root = typeof surface.render === "function" ? await surface.render({ input, state, surface: surface.descriptor }) : state;
    const document = isUIDocument(root) ? validateUIDocument(root) : createUIDocument(isNodeLike(root) ? root : fallbackNodeFromValue(root, surface.descriptor), { surfaceId: surface.descriptor.id, revision: 1 });
    this.instances.set(surface.descriptor.id, { document, revision: document.revision, state, openedAt: defaultTimestamp(), ownerExtensionId: surface.descriptor.ownerExtensionId });
    const result = await this._deliver("snapshot", surface.descriptor.id, document, options);
    this.counters.opened++;
    this._notify(surface.descriptor.id, { type: "opened", document, fallback: result.fallback === true });
    return { ...result, document: deepClone(document) };
  }

  async renderSurface(id, document, options = {}) {
    const surface = this._surface(id, options);
    this._authorizeSurfaceOperation(surface, "renderSurface", options, ["ui.render.components", "ui.surface." + surface.descriptor.kind]);
    const previous = this.instances.get(surface.descriptor.id);
    const rendered = isUIDocument(document) ? validateUIDocument({ ...document, surfaceId: surface.descriptor.id }) : createUIDocument(isNodeLike(document) ? document : fallbackNodeFromValue(document, surface.descriptor), { surfaceId: surface.descriptor.id, revision: previous?.revision ? previous.revision + 1 : 1 });
    this.instances.set(surface.descriptor.id, { document: rendered, revision: rendered.revision, state: document, openedAt: previous?.openedAt ?? defaultTimestamp(), ownerExtensionId: surface.descriptor.ownerExtensionId });
    const result = await this._deliver("snapshot", surface.descriptor.id, rendered, options);
    if (previous) this.counters.updated++; else this.counters.opened++;
    this._notify(surface.descriptor.id, { type: previous ? "updated" : "opened", document: rendered, fallback: result.fallback === true });
    return { ...result, document: deepClone(rendered) };
  }

  async update(id, next = {}, options = {}) {
    const surface = this._surface(id, options);
    this._authorizeSurfaceOperation(surface, "update", options, ["ui.render.components", "ui.surface." + surface.descriptor.kind]);
    const instance = this.instances.get(surface.descriptor.id);
    if (!instance) fail("ui.surfaceUnavailable", "Surface '" + surface.descriptor.id + "' is not open.");
    const root = typeof surface.render === "function" ? await surface.render({ input: next, state: next, previous: instance.document, surface: surface.descriptor }) : next;
    const document = isUIDocument(root) ? validateUIDocument(root) : createUIDocument(isNodeLike(root) ? root : fallbackNodeFromValue(root, surface.descriptor), { surfaceId: surface.descriptor.id, revision: instance.revision + 1 });
    this.instances.set(surface.descriptor.id, { ...instance, document, revision: document.revision });
    const result = await this._deliver("snapshot", surface.descriptor.id, document, options);
    this.counters.updated++;
    this._notify(surface.descriptor.id, { type: "updated", document, fallback: result.fallback === true });
    return { ...result, document: deepClone(document) };
  }

  async patch(id, patchDoc, options = {}) {
    const surface = this._surface(id, options);
    this._authorizeSurfaceOperation(surface, "patch", options, ["ui.render.components", "ui.surface." + surface.descriptor.kind]);
    const instance = this.instances.get(surface.descriptor.id);
    if (!instance) fail("ui.surfaceUnavailable", "Surface '" + surface.descriptor.id + "' is not open.");
    const validated = validatePatch({ ...patchDoc, surfaceId: surface.descriptor.id });
    const document = applyPatch(instance.document, validated);
    this.instances.set(surface.descriptor.id, { ...instance, document, revision: document.revision });
    const result = await this._deliver("patch", surface.descriptor.id, validated, options);
    this.counters.patched++;
    this._notify(surface.descriptor.id, { type: "patched", patch: validated, document, fallback: result.fallback === true });
    return { ...result, document: deepClone(document) };
  }

  patchSurface(id, patchDoc, options = {}) { return this.patch(id, patchDoc, options); }

  async close(id, options = {}) {
    const surface = this._surface(id, options);
    this._authorizeSurfaceOperation(surface, "close", options, ["ui.surface." + surface.descriptor.kind]);
    const instance = this.instances.get(surface.descriptor.id);
    if (!instance) return { ok: true, alreadyClosed: true, fallback: !this._interactive() };
    if (typeof surface.onClose === "function") await surface.onClose({ surface: surface.descriptor, document: instance.document });
    this.instances.delete(surface.descriptor.id);
    this.fallbacks.delete(surface.descriptor.id);
    let result = { ok: true };
    if (this.client) result = await this.client.lifecycle({ surfaceId: surface.descriptor.id, state: "disposed", reason: options.reason ?? "api", at: defaultTimestamp() }, options).catch((error) => this._fallback(surface.descriptor.id, instance.document, error));
    else if (this.bridge?.close) result = await this.bridge.close(surface.descriptor.id, options).catch((error) => this._fallback(surface.descriptor.id, instance.document, error));
    else if (!this._interactive()) result = this._fallback(surface.descriptor.id, instance.document, "broker-unavailable");
    this._publishObservation({ type: "ui.host.lifecycle", surfaceId: surface.descriptor.id, envelopeKind: "lifecycle", state: "disposed", reason: options.reason ?? "api", fallback: result.fallback === true });
    this.counters.closed++;
    this._notify(surface.descriptor.id, { type: "closed", reason: options.reason ?? "api", fallback: result.fallback === true });
    return result;
  }

  closeSurface(id, options = {}) { return this.close(id, options); }

  async invoke(id, actionId, parameters = {}, options = {}) {
    const surface = this._surface(id, options);
    this._authorizeSurfaceOperation(surface, "invoke", options, ["ui.action.invoke"]);
    const action = surface.actions.find((candidate) => (candidate.id ?? candidate.name) === actionId);
    if (!action) fail("ui.actionRejected", "Unknown action '" + actionId + "' for '" + surface.descriptor.id + "'.");
    this.counters.invoked++;
    this._publishObservation({ type: "ui.host.action", surfaceId: surface.descriptor.id, envelopeKind: "ui.event", attributes: { actionId, operation: "invoke" } });
    if (typeof action.handler === "function") return action.handler(parameters, this._controls(surface.descriptor.id, options));
    if (this.client) return this.client.action({ id: messageId("act"), actionId, surfaceId: surface.descriptor.id, parameters, requestedAt: defaultTimestamp() }, options);
    return { ok: true, fallback: !this._interactive() };
  }

  subscribe(id, listener, options = {}) {
    const surface = this._surface(id, options);
    this._authorizeSurfaceOperation(surface, "subscribe", options, ["ui.surface." + surface.descriptor.kind]);
    if (typeof listener !== "function") fail("ui.invalidEnvelope", "subscriber must be a function.");
    const set = this.subscribers.get(surface.descriptor.id) ?? new Set();
    set.add(listener);
    this.subscribers.set(surface.descriptor.id, set);
    return () => {
      set.delete(listener);
      if (set.size === 0) this.subscribers.delete(surface.descriptor.id);
    };
  }

  fallback(id, options = {}) {
    const surface = this._surface(id, options);
    return deepClone(this.fallbacks.get(surface.descriptor.id) ?? null);
  }

  registerObservabilitySink(sink, listener) {
    const descriptor = validateObservabilitySinkDescriptor(sink?.descriptor ?? sink);
    const ownerExtensionId = normalizeExtensionId(descriptor.extensionId ?? this.ownerExtensionId);
    this._assertCallerOwner(ownerExtensionId, descriptor.extensionId);
    const capability = descriptor.capability ?? "ui.observability.black-box.sink";
    this._authorize({ operation: "registerObservabilitySink", ownerExtensionId, sinkId: descriptor.id, resource: "afterburner.ui/envelopes/metadata", capability, descriptor });
    const publish = typeof listener === "function" ? listener : (typeof sink?.publish === "function" ? sink.publish.bind(sink) : (typeof sink?.observe === "function" ? sink.observe.bind(sink) : null));
    if (typeof publish !== "function") fail("ui.invalidEnvelope", "Observability sink '" + descriptor.id + "' requires publish().");
    const existing = this.observabilitySinks.get(descriptor.id);
    if (existing && existing.ownerExtensionId !== ownerExtensionId) authorizationDenied("Observability sink '" + descriptor.id + "' is owned by another extension.", { sinkId: descriptor.id, ownerExtensionId });
    if (existing) fail("ui.invalidEnvelope", "Observability sink '" + descriptor.id + "' is already registered.");
    const record = { id: descriptor.id, ownerExtensionId, descriptor: deepClone({ ...descriptor, extensionId: ownerExtensionId }), publish, delivered: 0, failures: 0, active: true };
    this.observabilitySinks.set(descriptor.id, record);
    this.counters.observabilitySinks = this.observabilitySinks.size;
    const unregister = () => {
      if (!record.active) return;
      record.active = false;
      this.observabilitySinks.delete(record.id);
      this.counters.observabilitySinks = this.observabilitySinks.size;
    };
    Object.defineProperty(unregister, "diagnostics", { enumerable: true, value: () => deepFreeze({ id: record.id, ownerExtensionId: record.ownerExtensionId, delivered: record.delivered, failures: record.failures, active: record.active }) });
    return unregister;
  }

  subscribeObservability(descriptor, listener) { return this.registerObservabilitySink({ descriptor, publish: listener }); }

  diagnostics(options = {}) {
    const owner = normalizeExtensionId(options.ownerExtensionId ?? this.ownerExtensionId);
    const surfaces = [...this.surfaces.values()].filter((surface) => !owner || surface.descriptor.ownerExtensionId === owner);
    return deepFreeze({
      schemaVersion: SCHEMA_VERSION,
      protocol: PROTOCOL,
      revision: PROTOCOL_REVISION,
      ownerExtensionId: owner,
      interactive: this._interactive(),
      ...this.counters,
      active: surfaces.filter((surface) => this.instances.has(surface.descriptor.id)).length,
      surfaces: surfaces.map((surface) => surface.descriptor.id).sort(),
      fallbacks: [...this.fallbacks.keys()].filter((id) => surfaces.some((surface) => surface.descriptor.id === id)).sort(),
      observabilitySinks: [...this.observabilitySinks.values()].filter((sink) => !owner || sink.ownerExtensionId === owner).map((sink) => sink.id).sort()
    });
  }

  _surface(id, options = {}) {
    validateStableId(id, "surface id");
    const surface = this.surfaces.get(id);
    if (!surface) fail("ui.surfaceUnavailable", "Unknown surface '" + id + "'.");
    this._assertSurfaceOwner(surface, normalizeExtensionId(options.ownerExtensionId ?? this.ownerExtensionId), "surface operation");
    return surface;
  }

  _controls(id, options = {}) {
    const ownerOptions = options.ownerExtensionId ? { ownerExtensionId: options.ownerExtensionId } : {};
    return { update: (next) => this.update(id, next, ownerOptions), patch: (patchDoc) => this.patch(id, patchDoc, ownerOptions), close: () => this.close(id, ownerOptions), fallback: () => this.fallback(id, ownerOptions), diagnostics: () => this.diagnostics(ownerOptions) };
  }

  async _deliver(kind, id, payload, options = {}) {
    try {
      let result;
      if (this.client) result = kind === "patch" ? await this.client.patch(payload, options) : await this.client.snapshot(payload, options);
      else if (this.bridge) {
        if (kind === "patch" && this.bridge.patch) result = await this.bridge.patch(id, payload, options);
        else if (this.bridge.render) result = await this.bridge.render(id, payload, options);
      }
      if (!result) result = this._fallback(id, kind === "patch" ? this.instances.get(id)?.document : payload, "broker-unavailable");
      else if (result.ok === true && result.fallback === undefined) result = { ...result, fallback: false };
      this._publishObservation({ type: kind === "patch" ? "ui.host.patch" : "ui.host.lifecycle", surfaceId: id, envelopeKind: kind === "patch" ? "component.patch" : "component.snapshot", state: kind === "patch" ? "patched" : "rendered", fallback: result.fallback === true });
      return result;
    } catch (error) {
      const result = this._fallback(id, kind === "patch" ? this.instances.get(id)?.document : payload, error);
      this._publishObservation({ type: "ui.host.recovery", surfaceId: id, envelopeKind: kind === "patch" ? "component.patch" : "component.snapshot", state: "fallback", reason: result.error, fallback: true });
      return result;
    }
  }

  _fallback(id, document, error) {
    this.counters.fallbackCount++;
    const projection = fallbackProjection(document, { error });
    this.fallbacks.set(id, projection);
    return { ok: false, fallback: true, text: projection.text, error: projection.error };
  }

  _interactive() { return Boolean(this.client?.connected || this.bridge?.interactive); }

  _notify(id, event) {
    for (const listener of this.subscribers.get(id) ?? []) queueMicrotask(() => listener(deepClone(event)));
  }

  _assertCallerOwner(ownerExtensionId, requestedOwner) {
    if (this.ownerExtensionId && requestedOwner !== undefined && ownerExtensionId !== this.ownerExtensionId) {
      authorizationDenied("Extension API cannot act for another ownerExtensionId.", { ownerExtensionId: this.ownerExtensionId, requestedOwnerExtensionId: ownerExtensionId });
    }
  }

  _assertSurfaceOwner(surface, callerOwner, operation) {
    const owner = surface?.descriptor?.ownerExtensionId;
    if (owner && callerOwner && owner !== callerOwner) authorizationDenied("Surface '" + surface.descriptor.id + "' " + operation + " is owned by another extension.", { surfaceId: surface.descriptor.id, ownerExtensionId: callerOwner, expectedOwnerExtensionId: owner });
    if (owner && this.ownerExtensionId && owner !== this.ownerExtensionId) authorizationDenied("Surface '" + surface.descriptor.id + "' is owned by another extension.", { surfaceId: surface.descriptor.id, ownerExtensionId: this.ownerExtensionId, expectedOwnerExtensionId: owner });
  }

  _authorize(request) {
    if (!this.enforceAuthorization) return;
    const decision = normalizeGrantDecision(this.authorize?.(request)) ?? { allowed: false, reason: "grant-denied" };
    if (!decision.allowed) authorizationDenied("UI operation '" + request.operation + "' was denied.", { ...request, reason: decision.reason });
  }

  _authorizeSurfaceOperation(surface, operation, options, capabilities) {
    this._authorize({ operation, ownerExtensionId: surface.descriptor.ownerExtensionId ?? options.ownerExtensionId ?? this.ownerExtensionId, surfaceId: surface.descriptor.id, resource: surface.descriptor.id, capabilities, descriptor: surface.descriptor });
  }

  _publishObservation(observation) {
    if (this.observabilitySinks.size === 0) return;
    const sanitized = metadataOnlyObservation(observation);
    this.counters.observations++;
    for (const sink of this.observabilitySinks.values()) {
      if (!sink.active) continue;
      try {
        Promise.resolve(sink.publish(sanitized)).then(() => { sink.delivered++; }, () => { sink.failures++; });
      } catch {
        sink.failures++;
      }
    }
  }
}
function isUIDocument(value) { return value && typeof value === "object" && value.root && value.surfaceId; }
function fallbackNodeFromValue(value, descriptor) {
  if (isNodeLike(value)) return value;
  if (typeof value === "string") return dialog({ title: descriptor.title ?? descriptor.id }, [markdown(value)]);
  if (value && typeof value === "object") return modalFrameToUIDocument(descriptor, value).root;
  return dialog({ title: descriptor.title ?? descriptor.id }, [text("")]);
}

export function createRuntime(options = {}) { return new UIRuntime(options); }

export function createExtensionBridge(options = {}) {
  const ownerExtensionId = normalizeExtensionId(options.ownerExtensionId ?? options.extensionId ?? options.manifest?.id);
  const hostBridge = options.bridge ?? null;
  const runtime = options.runtime ?? new UIRuntime({ ...options, bridge: hostBridge, ownerExtensionId, denyByDefault: options.denyByDefault ?? true });
  const ownerOptions = ownerExtensionId ? { ownerExtensionId } : {};
  const decide = (request) => normalizeGrantDecision(runtime.authorize?.(request)) ?? { allowed: false, reason: "grant-denied" };
  const bridge = {
    ownerExtensionId,
    runtime,
    registerSurface: (descriptor) => {
      const handle = runtime.registerSurface({ ...descriptor, ownerExtensionId: descriptor?.ownerExtensionId ?? ownerExtensionId });
      return { ok: true, handle, descriptor: handle.descriptor };
    },
    renderSurface: (id, document, callOptions = {}) => runtime.renderSurface(id, document, { ...callOptions, ...ownerOptions }),
    patchSurface: (id, patchDoc, callOptions = {}) => runtime.patchSurface(id, patchDoc, { ...callOptions, ...ownerOptions }),
    closeSurface: (id, callOptions = {}) => runtime.closeSurface(id, { ...callOptions, ...ownerOptions }),
    registerObservabilitySink: (sink, listener) => runtime.registerObservabilitySink(sink, listener),
    subscribeObservability: (descriptor, listener) => runtime.subscribeObservability(descriptor, listener),
    diagnostics: () => runtime.diagnostics(ownerOptions),
    requestCapabilities: async (declarations = []) => {
      const denied = [];
      const optionalDenied = [];
      for (const declaration of declarations) {
        const capability = validateCapability(declaration?.capability, "requested");
        const optional = declaration?.optional === true || declaration?.required === false || declaration?.requirement === "optional";
        const resources = Array.isArray(declaration?.resources) && declaration.resources.length ? declaration.resources : [declaration?.resource ?? "*"];
        for (const resource of resources) {
          const decision = decide({ operation: "requestCapabilities", ownerExtensionId, capability, resource });
          if (!decision.allowed) (optional ? optionalDenied : denied).push({ capability, resource, reason: decision.reason });
        }
      }
      return denied.length ? { granted: false, denied, optionalDenied } : { granted: true, ...(optionalDenied.length ? { optionalDenied } : {}) };
    },
    hasCapability: (capability, resource) => decide({ operation: "hasCapability", ownerExtensionId, capability: validateCapability(capability, "requested"), ...(resource === undefined ? {} : { resource }) }).allowed
  };
  bridge.ui = Object.freeze({
    runtime,
    registerSurface: bridge.registerSurface,
    renderSurface: bridge.renderSurface,
    patchSurface: bridge.patchSurface,
    closeSurface: bridge.closeSurface,
    registerObservabilitySink: bridge.registerObservabilitySink,
    subscribeObservability: bridge.subscribeObservability,
    diagnostics: bridge.diagnostics
  });
  return Object.freeze(bridge);
}

export const defaultRuntime = new UIRuntime();

export function validatePatch(patch) {
  assertObject(patch, "patch");
  if (patch.schemaVersion !== undefined && patch.schemaVersion !== SCHEMA_VERSION) fail("ui.invalidPatch", "Patch schemaVersion is unsupported.");
  if (patch.protocol !== undefined && patch.protocol !== PROTOCOL) fail("ui.invalidPatch", "Patch protocol is unsupported.");
  validateStableId(patch.surfaceId, "patch surfaceId");
  if (!Number.isSafeInteger(Number(patch.baseRevision ?? 0)) || !Number.isSafeInteger(Number(patch.nextRevision ?? 0))) fail("ui.invalidPatch", "Patch revisions must be integers.");
  if (!Array.isArray(patch.operations)) fail("ui.invalidPatch", "Patch operations must be an array.");
  for (const op of patch.operations) {
    assertObject(op, "patch operation");
    if (!["add", "remove", "replace", "move", "copy", "test", "setProps", "bindData", "bindAction"].includes(op.op)) fail("ui.invalidPatch", `Unsupported patch op '${String(op.op)}'.`);
    if (typeof op.path !== "string" || !op.path.startsWith("/")) fail("ui.invalidPatch", "Patch op path must be a JSON pointer.");
  }
  return { schemaVersion: SCHEMA_VERSION, protocol: PROTOCOL, revision: PROTOCOL_REVISION, ...deepClone(patch) };
}

export function applyPatch(document, patch) {
  const validated = validatePatch(patch);
  const next = deepClone(validateUIDocument(document));
  if (Number(validated.baseRevision) !== Number(document.revision)) fail("ui.invalidPatch", "Patch baseRevision does not match document revision.");
  for (const op of validated.operations) applyPatchOp(next, op);
  next.revision = Number(validated.nextRevision);
  validateUIDocument(next);
  return deepFreeze(next);
}

function applyPatchOp(root, op) {
  if (op.op === "test") {
    const actual = getPointer(root, op.path);
    if (JSON.stringify(canonicalize(actual)) !== JSON.stringify(canonicalize(op.value))) fail("ui.invalidPatch", `Patch test failed at ${op.path}.`);
    return;
  }
  if (op.op === "move") {
    const value = getPointer(root, op.from);
    removePointer(root, op.from);
    setPointer(root, op.path, value, true);
    return;
  }
  if (op.op === "copy") { setPointer(root, op.path, deepClone(getPointer(root, op.from)), true); return; }
  if (op.op === "remove") { removePointer(root, op.path); return; }
  if (op.op === "setProps") {
    const node = getPointer(root, op.path);
    if (!node || typeof node !== "object") fail("ui.invalidPatch", `setProps target '${op.path}' is not an object.`);
    node.props = { ...(node.props ?? {}), ...(op.value ?? {}) };
    return;
  }
  if (op.op === "bindData") {
    const node = getPointer(root, op.path);
    node.dataBindings = [...new Set([...(node.dataBindings ?? []), op.value])].filter(Boolean);
    return;
  }
  if (op.op === "bindAction") {
    const node = getPointer(root, op.path);
    node.actionBindings = { ...(node.actionBindings ?? {}), ...(op.value ?? {}) };
    return;
  }
  setPointer(root, op.path, op.value, op.op === "add");
}

function pointerParts(pointer) {
  if (pointer === "") return [];
  return pointer.split("/").slice(1).map((part) => part.replace(/~1/g, "/").replace(/~0/g, "~"));
}
function getPointer(root, pointer) {
  let target = root;
  for (const part of pointerParts(pointer)) target = target?.[Array.isArray(target) && part !== "-" ? Number(part) : part];
  return target;
}
function parentPointer(root, pointer) {
  const parts = pointerParts(pointer);
  const key = parts.pop();
  let parent = root;
  for (const part of parts) parent = parent?.[Array.isArray(parent) ? Number(part) : part];
  if (parent === undefined || key === undefined) fail("ui.invalidPatch", `Invalid patch path '${pointer}'.`);
  return { parent, key };
}
function setPointer(root, pointer, value, add = false) {
  const { parent, key } = parentPointer(root, pointer);
  if (Array.isArray(parent)) {
    if (key === "-") parent.push(value);
    else if (add) parent.splice(Number(key), 0, value);
    else parent[Number(key)] = value;
  } else parent[key] = value;
}
function removePointer(root, pointer) {
  const { parent, key } = parentPointer(root, pointer);
  if (Array.isArray(parent)) parent.splice(Number(key), 1);
  else delete parent[key];
}

export function modalFrameToUIDocument(canvasOrDescriptor, frame = {}, options = {}) {
  const descriptor = typeof canvasOrDescriptor === "string" ? { id: canvasOrDescriptor, title: canvasOrDescriptor } : (canvasOrDescriptor ?? {});
  const surfaceId = descriptor.id ?? frame.id ?? "modal";
  if (isUIDocument(frame?.document)) {
    return validateUIDocument({ ...frame.document, surfaceId, revision: options.revision ?? frame.document.revision ?? 1 });
  }
  const title = truncate(frame.title ?? descriptor.displayName ?? descriptor.title ?? surfaceId, 256);
  const status = truncate(frame.status ?? "", 2048);
  const body = modalText(frame.body ?? frame.text ?? frame.markdown ?? frame.lines ?? "");
  const footer = truncate(frame.footer ?? "Esc/q closes · Copilot keeps running in the background", 2048);
  const actions = (frame.actions ?? descriptor.actions ?? []).map((action) => button({ label: action.label ?? action.title ?? action.name ?? action.id, actionId: action.id ?? action.name, keybinding: action.key, description: action.description }, [], { id: `${surfaceId}:action:${action.id ?? action.name}`.replace(/[^a-zA-Z0-9._:-]/g, "-").toLowerCase() }));
  const root = dialog({ title, status, modal: true }, [
    ...(status ? [text({ value: status, tone: "muted" }, [], { id: `${surfaceId}:status`.toLowerCase() })] : []),
    markdown({ markdown: body }, [], { id: `${surfaceId}:body`.toLowerCase() }),
    ...(actions.length ? [toolbar({ label: "Actions" }, actions, { id: `${surfaceId}:actions`.toLowerCase() })] : []),
    ...(footer ? [text({ value: footer, tone: "muted" }, [], { id: `${surfaceId}:footer`.toLowerCase() })] : [])
  ], { id: `${surfaceId}:dialog`.toLowerCase(), accessibility: { role: "dialog", name: title } });
  return createUIDocument(root, { surfaceId, revision: options.revision ?? 1, themeId: options.themeId, locale: options.locale });
}

function modalText(value) {
  if (typeof value === "string") return value;
  if (Array.isArray(value?.lines)) return value.lines.map(String).join("\n");
  if (typeof value?.text === "string") return value.text;
  if (typeof value?.markdown === "string") return value.markdown;
  if (value && typeof value === "object") return JSON.stringify(value, null, 2);
  return "";
}

export function fallbackProjection(document, options = {}) {
  const doc = isUIDocument(document) ? document : createUIDocument(fallbackNodeFromValue(document, { id: "fallback", title: "Fallback" }), { surfaceId: "fallback" });
  const lines = [];
  projectNode(doc.root, lines, 0);
  const text = lines.join("\n").replace(/\n{3,}/g, "\n\n").trim();
  const error = typeof options.error === "string" ? options.error : options.error?.code ?? options.error?.name ?? null;
  return deepFreeze({ schemaVersion: SCHEMA_VERSION, surfaceId: doc.surfaceId, revision: doc.revision, text, document: deepClone(doc), error, updatedAt: defaultTimestamp() });
}

function projectNode(node, lines, depth) {
  const props = node.props ?? {};
  const indent = "  ".repeat(depth);
  if (node.kind === "dialog" || node.kind === "window" || node.kind === "application") {
    if (props.title) lines.push(`${indent}# ${props.title}`);
  } else if (node.kind === "text") lines.push(`${indent}${props.value ?? props.text ?? ""}`);
  else if (node.kind === "markdown") lines.push(`${indent}${props.markdown ?? props.value ?? ""}`);
  else if (node.kind === "code") lines.push(`${indent}\`\`\`${props.language ?? ""}\n${props.code ?? props.value ?? ""}\n\`\`\``);
  else if (node.kind === "button") lines.push(`${indent}[${props.label ?? node.id}]${props.keybinding ? ` (${props.keybinding})` : ""}`);
  else if (props.label) lines.push(`${indent}${props.label}`);
  for (const child of node.children ?? []) projectNode(child, lines, depth + (node.kind === "row" || node.kind === "toolbar" ? 0 : 1));
}

export function createModalCanvasCompatibility(legacy = {}) {
  const runtime = legacy.runtime ?? new UIRuntime({ bridge: legacy.bridge, client: legacy.client, ownerExtensionId: legacy.ownerExtensionId, manifest: legacy.manifest, grantResolver: legacy.grantResolver, authorize: legacy.authorize, denyByDefault: legacy.denyByDefault });
  const ownerExtensionId = normalizeExtensionId(legacy.ownerExtensionId ?? runtime.ownerExtensionId);
  function registerModalCanvas(definition) {
    assertObject(definition, "modal canvas definition");
    runtime._authorize({ operation: "modalCanvas", ownerExtensionId, surfaceId: definition.id, resource: definition.id, capability: "ui.surface.modal" });
    const descriptorActions = (definition.actions ?? []).map((action) => ({ ...action, id: action.name, title: action.label, effect: action.name === "close" ? "dismiss" : "execute" }));
    const ownerOptions = ownerExtensionId ? { ownerExtensionId } : {};
    const surfaceHandle = runtime.defineSurface({
      id: definition.id,
      kind: "modal",
      ownerExtensionId,
      title: definition.displayName ?? definition.id,
      displayName: definition.displayName,
      description: definition.description,
      actions: descriptorActions,
      open: definition.open,
      render: async (context) => modalFrameToUIDocument({ id: definition.id, displayName: definition.displayName, actions: definition.actions }, typeof definition.render === "function" ? await definition.render(context) : context.state, { revision: context.previous?.revision ? context.previous.revision + 1 : 1 }),
      close: definition.close
    });
    return deepFreeze({
      id: definition.id,
      open: async (input) => modalResultToLegacy(await surfaceHandle.open(input, ownerOptions)),
      update: async (next) => modalResultToLegacy(await surfaceHandle.update(next, ownerOptions)),
      close: () => surfaceHandle.close({ ...ownerOptions }),
      invoke: (name, input) => surfaceHandle.invoke(name, input, ownerOptions),
      subscribe: (listener) => surfaceHandle.subscribe((event) => listener(modalEventToLegacy(event)), ownerOptions),
      fallback: () => {
        const value = surfaceHandle.fallback(ownerOptions);
        return value ? { id: definition.id, text: value.text, document: value.document, error: value.error, updatedAt: value.updatedAt } : null;
      },
      diagnostics: () => surfaceHandle.diagnostics(),
      document: () => runtime.instances.get(definition.id)?.document ?? null,
      dispose: async () => { await surfaceHandle.close({ reason: "dispose", ...ownerOptions }); runtime.surfaces.delete(definition.id); }
    });
  }
  return { registerModalCanvas, runtime };
}

function modalResultToLegacy(result) {
  const frame = uidocumentToModalFrame(result.document);
  return { ok: result.ok !== false, fallback: result.fallback === true, text: result.text, error: result.error, frame, document: result.document };
}
function uidocumentToModalFrame(document) {
  if (!document) return null;
  const root = document.root;
  const texts = [];
  projectNode(root, texts, 0);
  return { id: document.surfaceId, title: root.props?.title ?? document.surfaceId, body: texts.join("\n").trim(), actions: [] };
}
function modalEventToLegacy(event) {
  return { type: event.type === "opened" ? "opened" : event.type, generation: event.document?.revision, document: event.document, frame: uidocumentToModalFrame(event.document), fallback: event.fallback };
}

const defaultSidecarIsolationPolicy = Object.freeze({
  failClosed: true,
  process: Object.freeze({ requireJobObject: false, maxProcesses: 0, maxProcessMemoryBytes: 0, maxJobMemoryBytes: 0, maxCpuTimeMillis: 0, killOnClose: true }),
  filesystem: Object.freeze({ mode: "inherit", readRoots: [], writeRoots: [], denyGlobs: [] }),
  network: Object.freeze({ mode: "inherit", allowedHosts: [], deniedPorts: [] }),
  environment: Object.freeze({ clearInherited: true })
});

function sidecarIsolationCapabilities(adapter) {
  const provided = typeof adapter?.capabilities === "function" ? adapter.capabilities() : (adapter?.capabilities ?? {});
  const jobObject = provided.jobObject === true;
  const processTreeContainment = provided.processTreeContainment === true || process.platform !== "win32";
  return Object.freeze({
    platform: process.platform,
    restrictedEnvironment: true,
    processTreeCleanup: true,
    processTreeContainment,
    jobObject,
    resourceLimits: provided.resourceLimits === true,
    filesystemBoundary: provided.filesystemBoundary === true,
    networkBoundary: provided.networkBoundary === true
  });
}

function normalizeSidecarIsolationPolicy(options) {
  const input = options.isolationPolicy ?? options.resourcePolicy ?? {};
  const processPolicy = { ...defaultSidecarIsolationPolicy.process, ...(input.process ?? {}) };
  if (options.enforceJobObject === true || options.requireJobObject === true) processPolicy.requireJobObject = true;
  const filesystem = { ...defaultSidecarIsolationPolicy.filesystem, ...(input.filesystem ?? options.filesystem ?? {}) };
  const network = { ...defaultSidecarIsolationPolicy.network, ...(input.network ?? options.network ?? {}) };
  const environment = { ...defaultSidecarIsolationPolicy.environment, ...(input.environment ?? {}) };
  return deepFreeze({ failClosed: input.failClosed !== false, process: processPolicy, filesystem, network, environment });
}

function missingSidecarIsolationCapabilities(policy, capabilities) {
  const missing = [];
  if (policy.environment?.clearInherited && !capabilities.restrictedEnvironment) missing.push("environment.restricted");
  if (process.platform === "win32" && policy.process?.requireJobObject && !capabilities.jobObject) missing.push("process.jobObject");
  if (policy.process?.killOnClose === true && !capabilities.processTreeCleanup) missing.push("process.treeCleanup");
  if (policy.process?.maxProcesses > 0 && !capabilities.processTreeContainment) missing.push("process.jobObject");
  if ((policy.process?.maxProcessMemoryBytes > 0 || policy.process?.maxJobMemoryBytes > 0 || policy.process?.maxCpuTimeMillis > 0) && !capabilities.resourceLimits) missing.push("process.resourceLimits");
  if ((policy.filesystem?.mode ?? "inherit") !== "inherit" && !capabilities.filesystemBoundary) missing.push("filesystem." + policy.filesystem.mode);
  if ((policy.network?.mode ?? "inherit") !== "inherit" && !capabilities.networkBoundary) missing.push("network." + policy.network.mode);
  return missing;
}

export class SidecarHost extends EventEmitter {
  constructor(options = {}) {
    super();
    this.options = { heartbeatMs: 1000, heartbeatTimeoutMs: 3000, queueLimit: 64, envAllowList: [], stdio: ["pipe", "pipe", "pipe", "ipc"], ...options };
    this.child = null;
    this.queue = [];
    this.pending = new Map();
    this.lastHeartbeat = 0;
    this.heartbeatTimer = null;
    this.started = false;
    this.isolationPolicy = normalizeSidecarIsolationPolicy(this.options);
    this.isolationCapabilities = sidecarIsolationCapabilities(this.options.isolationAdapter);
    this.isolationHandle = null;
  }

  start() {
    if (this.started) return this;
    if (!this.options.entrypoint) throw new UIContractError("ui.invalidEnvelope", "Sidecar entrypoint is required.");
    const missing = missingSidecarIsolationCapabilities(this.isolationPolicy, this.isolationCapabilities);
    if (missing.length && this.isolationPolicy.failClosed !== false) {
      throw new UIContractError("ui.policyDenied", "Sidecar isolation unavailable: " + missing.join(", ") + ".");
    }
    if (typeof this.options.isolationAdapter?.prepare === "function") {
      this.isolationHandle = this.options.isolationAdapter.prepare({ policy: this.isolationPolicy, capabilities: this.isolationCapabilities });
    }
    const env = {};
    for (const key of this.options.envAllowList) if (process.env[key] !== undefined) env[key] = process.env[key];
    env.AFTERBURNER_UI_SIDECAR = "1";
    this.child = spawn(process.execPath, [this.options.entrypoint, ...(this.options.args ?? [])], {
      cwd: this.options.cwd,
      env,
      stdio: this.options.stdio,
      windowsHide: true,
      detached: this.options.detached ?? process.platform !== "win32"
    });
    this.started = true;
    this.lastHeartbeat = Date.now();
    this.child.on("message", (message) => this._message(message));
    this.child.once("exit", (code, signal) => this._exit(code, signal));
    this.child.once("error", (error) => this._exit(null, null, error));
    this.heartbeatTimer = setInterval(() => this._heartbeat(), this.options.heartbeatMs);
    this._send({ type: "hello", id: messageId("sidecar"), policy: this.policyInterface() });
    return this;
  }

  stop(reason = "api") {
    clearInterval(this.heartbeatTimer);
    this.heartbeatTimer = null;
    const child = this.child;
    if (child && !child.killed) this._terminateProcessTree(child);
    this._rejectAll(new UITransportError("ui.transport.canceled", "sidecar stopped: " + reason, { recoverable: true }));
  }

  call(method, payload = {}, options = {}) {
    this.start();
    if (this.queue.length >= this.options.queueLimit) return Promise.reject(new UITransportError("ui.transport.backpressure", "sidecar queue limit reached", { recoverable: true }));
    const id = messageId("sidecar-call");
    const promise = new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject, createdAt: Date.now() });
      this.queue.push({ type: "call", id, method, payload });
      this._drain();
    });
    return withAbort(promise, options.signal);
  }

  policyInterface() {
    return deepFreeze({
      isolation: this.isolationPolicy,
      capabilities: this.isolationCapabilities,
      enforceJobObject: this.isolationPolicy.process.requireJobObject === true && this.isolationCapabilities.jobObject === true,
      restrictEnvironment: this.isolationCapabilities.restrictedEnvironment,
      envAllowList: [...this.options.envAllowList],
      queueLimit: this.options.queueLimit,
      heartbeatMs: this.options.heartbeatMs,
      heartbeatTimeoutMs: this.options.heartbeatTimeoutMs,
      lifecycle: ["starting", "running", "stopping", "exited", "failed"]
    });
  }

  diagnostics() {
    return deepFreeze({ running: this.started && this.child && !this.child.killed, pid: this.child?.pid ?? null, queueDepth: this.queue.length, pending: this.pending.size, lastHeartbeat: this.lastHeartbeat, policy: this.policyInterface() });
  }

  _send(message) { if (this.child?.connected) this.child.send(message); }
  _drain() { while (this.queue.length) this._send(this.queue.shift()); }
  _message(message) {
    if (message?.type === "heartbeat") { this.lastHeartbeat = Date.now(); this.emit("heartbeat", message); return; }
    if (message?.type === "ready") { this.emit("ready", message); return; }
    if (message?.id && this.pending.has(message.id)) {
      const pending = this.pending.get(message.id);
      this.pending.delete(message.id);
      if (message.type === "error") pending.reject(new UITransportError(message.code ?? "ui.actionFailed", message.message ?? "sidecar call failed"));
      else pending.resolve(message.payload);
    }
  }
  _heartbeat() {
    if (!this.child || this.child.killed) return;
    if (Date.now() - this.lastHeartbeat > this.options.heartbeatTimeoutMs) {
      this.emit("crash", new UITransportError("ui.transport.deadlineExceeded", "sidecar heartbeat timed out", { recoverable: true }));
      this.stop("heartbeat-timeout");
      return;
    }
    this._send({ type: "heartbeat", at: defaultTimestamp() });
  }
  _exit(code, signal, error) {
    clearInterval(this.heartbeatTimer);
    this.started = false;
    if (typeof this.isolationHandle?.cleanup === "function") {
      try { this.isolationHandle.cleanup(); } catch {}
    }
    this.isolationHandle = null;
    const err = error ?? new UITransportError("ui.surfaceUnavailable", "sidecar exited (" + (code ?? signal ?? "unknown") + ")", { recoverable: true });
    this._rejectAll(err);
    this.emit("exit", { code, signal, error: err });
  }
  _rejectAll(error) {
    for (const pending of this.pending.values()) pending.reject(error);
    this.pending.clear();
    this.queue.length = 0;
  }
  _terminateProcessTree(child) {
    if (typeof this.isolationHandle?.terminate === "function") {
      try { this.isolationHandle.terminate(child.pid); return; } catch {}
    }
    if (!child.pid) return child.kill();
    if (process.platform === "win32") {
      try {
        const killer = spawn("taskkill.exe", ["/pid", String(child.pid), "/T", "/F"], { stdio: "ignore", windowsHide: true });
        killer.unref();
        return;
      } catch {}
      child.kill();
      return;
    }
    try { process.kill(-child.pid, "SIGTERM"); } catch { child.kill(); }
    const timer = setTimeout(() => { try { process.kill(-child.pid, "SIGKILL"); } catch {} }, 1000);
    timer.unref?.();
  }
}

export function createSidecarHost(options) { return new SidecarHost(options); }

export function installSidecarRuntime(handlers = {}) {
  if (typeof process.send !== "function") throw new UIContractError("ui.surfaceUnavailable", "sidecar runtime requires IPC");
  const heartbeat = setInterval(() => process.send({ type: "heartbeat", at: defaultTimestamp() }), Number(handlers.heartbeatMs ?? 1000));
  process.on("disconnect", () => { clearInterval(heartbeat); process.exit(0); });
  process.on("message", async (message) => {
    if (message?.type === "hello") { process.send({ type: "ready", id: message.id }); return; }
    if (message?.type === "heartbeat") { process.send({ type: "heartbeat", at: defaultTimestamp() }); return; }
    if (message?.type !== "call") return;
    try {
      const handler = handlers[message.method];
      if (typeof handler !== "function") throw new UIContractError("ui.actionRejected", `Unknown sidecar method '${message.method}'.`);
      const payload = await handler(message.payload);
      process.send({ type: "result", id: message.id, payload });
    } catch (error) {
      process.send({ type: "error", id: message.id, code: error.code ?? "ui.actionFailed", message: error.message ?? String(error) });
    }
  });
  process.send({ type: "ready" });
  return () => clearInterval(heartbeat);
}

export function createTestHost(options = {}) {
  const [clientTransport, hostTransport] = MemoryTransport.pair();
  const host = new EventEmitter();
  host.received = [];
  host.epoch = Number(options.epoch ?? 1);
  host.generation = Number(options.generation ?? 1);
  hostTransport.on("message", (envelope) => {
    host.received.push(envelope);
    if (envelope.kind === "hello") {
      hostTransport.write(createEnvelope("hello.result", { ...negotiateHello(createHello({ endpoint: { name: "host" }, epoch: host.epoch, generation: host.generation }), envelope.payload), accepted: true }, { epoch: host.epoch, generation: host.generation, sequence: 1, source: { kind: "host", id: "test-host" }, target: envelope.source }));
      return;
    }
    if (options.backpressure && host.received.length === 2) {
      hostTransport.write(createEnvelope("backpressure", { active: true, reason: "test", queueDepth: 1, queueLimit: 1 }, { epoch: host.epoch, generation: host.generation, sequence: 2, source: { kind: "host", id: "test-host" }, target: envelope.source }));
    }
    hostTransport.write(createEnvelope("ack", {}, { epoch: host.epoch, generation: host.generation, sequence: host.received.length + 10, source: { kind: "host", id: "test-host" }, target: envelope.source, ack: { epoch: host.epoch, highWater: envelope.sequence ?? 0, contiguous: true } }));
  });
  host.clientTransport = clientTransport;
  host.hostTransport = hostTransport;
  host.sendStale = () => hostTransport.write(createEnvelope("ack", {}, { epoch: host.epoch - 1, sequence: 99, ack: { epoch: host.epoch - 1, highWater: 999, contiguous: true } }));
  host.releaseBackpressure = () => hostTransport.write(createEnvelope("backpressure", { active: false }, { epoch: host.epoch, sequence: 100 }));
  host.crash = () => hostTransport.close();
  return host;
}

export default {
  PROTOCOL, PROTOCOL_REVISION, COMPATIBILITY_ID, SCHEMA_VERSION,
  componentKinds, componentCatalog, components, createNode, createUIDocument, validateUIDocument, validateNode,
  surfaceKinds, surfaceCatalog, capabilityKinds, capabilityCatalog, defineSurface, registerSurface, open, renderSurface, close, closeSurface, update, patch, patchSurface, invoke, subscribe, fallback,
  registerObservabilitySink, subscribeObservability, diagnostics,
  createRuntime, createExtensionBridge, createProtocolClient, ProtocolClient, MemoryTransport, FrameTransport, createFrameTransport, createEnvelope, validateEnvelope,
  createHello, negotiateHello, encodeFrame, decodeFrame, validatePatch, applyPatch, fallbackProjection,
  modalFrameToUIDocument, createModalCanvasCompatibility, SidecarHost, createSidecarHost, installSidecarRuntime, createTestHost
};
