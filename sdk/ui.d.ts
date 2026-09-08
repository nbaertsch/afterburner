export const UI_PROTOCOL: "afterburner.ui";
export const UI_REVISION: 1;

export type UIPrimitive = string | number | boolean | null;
export type UIValue = UIPrimitive | UIValue[] | { [key: string]: UIValue };

export type ComponentKind =
  | "dialog" | "application" | "surface" | "window" | "viewport"
  | "stack" | "row" | "column" | "group" | "grid" | "statusGrid" | "split"
  | "section" | "box" | "disclosure" | "panel" | "card" | "scroll" | "toolbar"
  | "separator" | "spacer" | "breadcrumb" | "contextMenu" | "icon"
  | "text" | "markdown" | "code" | "keyValue" | "detail" | "badge" | "alert"
  | "toast" | "progress" | "meter" | "bar" | "sparkline" | "spinner" | "loading"
  | "errorBoundary" | "empty" | "list" | "table" | "tree" | "timeline" | "tabs"
  | "log" | "commandPalette" | "form" | "button" | "link" | "textInput"
  | "searchInput" | "numberInput" | "dateInput" | "fileInput" | "passwordInput"
  | "textArea" | "select" | "checkbox" | "radioGroup" | "toggle" | "slider"
  | "actionBar" | "keybindingHint" | "pagination" | "help" | "confirmation"
  | "prompt";

export interface UINode {
  id?: string;
  kind: ComponentKind;
  props?: Record<string, UIValue>;
  children?: UINode[];
  accessibility?: Record<string, UIValue>;
  actionBindings?: Record<string, UIValue>;
  metadata?: Record<string, UIValue>;
}

export interface UIDocument {
  schemaVersion: 1;
  protocol: "afterburner.ui";
  revision: number;
  surfaceId: string;
  root: UINode;
  locale?: string;
}

export interface UIEvent {
  type: "activate" | "change" | "submit" | "focus" | "blur";
  targetId: string;
  documentRevision: number;
  generation: number;
  sequence: number;
  key?: string;
  actionName?: string;
  value?: UIValue;
}

export interface SurfaceControls {
  update(next: unknown): Promise<unknown>;
  close(): Promise<unknown>;
  invoke(name: string, input?: unknown): Promise<unknown>;
}

export interface ModalSurfaceDefinition {
  id: string;
  kind?: "modal";
  displayName?: string;
  description?: string;
  actions?: Array<{
    name: string;
    label?: string;
    key?: string;
    description?: string;
    handler?: (input: unknown, controls: SurfaceControls) => unknown | Promise<unknown>;
  }>;
  open?: (input: unknown) => unknown | Promise<unknown>;
  render?: (context: { input: unknown; state: unknown }) => unknown | Promise<unknown>;
  subscribe?: (controls: SurfaceControls) => void | (() => void) | Promise<void | (() => void)>;
  onEvent?: (event: UIEvent, controls: SurfaceControls) => void | Promise<void>;
}

export interface RegisteredSurface {
  readonly id: string;
  readonly ownerExtensionId?: string;
  open(input?: unknown): Promise<unknown>;
  update(next?: unknown): Promise<unknown>;
  close(): Promise<unknown>;
  invoke(name: string, input?: unknown): Promise<unknown>;
  subscribe(listener: (event: unknown) => void): () => void;
  fallback(): unknown;
  diagnostics(): unknown;
  dispose(): Promise<void>;
}

export interface UIAPI {
  readonly UI_PROTOCOL: typeof UI_PROTOCOL;
  readonly UI_REVISION: typeof UI_REVISION;
  readonly componentKinds: readonly ComponentKind[];
  readonly components: Record<ComponentKind, (
    props?: Record<string, UIValue>,
    children?: UINode[],
    options?: Pick<UINode, "id" | "accessibility" | "actionBindings" | "metadata">
  ) => UINode>;
  createDocument(root: UINode, options: { surfaceId: string; revision?: number; locale?: string }): UIDocument;
  createUIDocument(root: UINode, options: { surfaceId: string; revision?: number; locale?: string }): UIDocument;
  validateDocument(document: UIDocument): UIDocument;
  validateModalDocument(document: UIDocument): UIDocument;
  registerSurface(definition: ModalSurfaceDefinition): RegisteredSurface;
}

export const componentKinds: readonly ComponentKind[];
export const components: UIAPI["components"];
export function createDocument(root: UINode, options: { surfaceId: string; revision?: number; locale?: string }): UIDocument;
export function createUIDocument(root: UINode, options: { surfaceId: string; revision?: number; locale?: string }): UIDocument;
export function validateDocument(document: UIDocument): UIDocument;
export function validateModalDocument(document: UIDocument): UIDocument;
