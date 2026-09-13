const schemaMaps = ["properties", "patternProperties", "$defs", "definitions", "dependentSchemas"];
const schemaValues = [
    "additionalProperties", "unevaluatedProperties", "propertyNames", "items",
    "additionalItems", "contains", "not", "if", "then", "else", "unevaluatedItems", "contentSchema"
];
const schemaArrays = ["allOf", "anyOf", "oneOf", "prefixItems"];
const object = value => value !== null && typeof value === "object" && !Array.isArray(value);
const pointerPart = value => value.replace(/~/g, "~0").replace(/\//g, "~1");

function depth(value) {
    if (value === null || typeof value !== "object") return 0;
    return 1 + Math.max(0, ...Object.values(value).map(depth));
}

function visitChildren(schema, visit, path) {
    for (const keyword of schemaMaps) {
        if (!object(schema[keyword])) continue;
        for (const name of Object.keys(schema[keyword])) {
            schema[keyword][name] = visit(schema[keyword][name], `${path}/${keyword}/${pointerPart(name)}`);
        }
    }
    for (const keyword of [...schemaValues, ...schemaArrays]) {
        const value = schema[keyword];
        if (Array.isArray(value)) {
            schema[keyword] = value.map((child, index) => visit(child, `${path}/${keyword}/${index}`));
        } else if (object(value)) {
            schema[keyword] = visit(value, `${path}/${keyword}`);
        }
    }
    if (object(schema.dependencies)) {
        for (const name of Object.keys(schema.dependencies)) {
            if (object(schema.dependencies[name])) {
                schema.dependencies[name] = visit(schema.dependencies[name], `${path}/dependencies/${pointerPart(name)}`);
            }
        }
    }
}

export function flattenToolSchema(schema) {
    if (!object(schema) || depth(schema) <= 10) return schema;
    const root = structuredClone(schema);
    const definitions = {};
    const moves = [];
    let index = 0;
    function flatten(value, path) {
        if (!object(value)) return value;
        if (path !== "#" && (value.$id !== undefined || value.id !== undefined)) {
            throw new Error("legacyTools cannot relocate a deeply nested schema with its own $id or id.");
        }
        visitChildren(value, flatten, path);
        if (path === "#" || (Object.keys(value).length === 1 && typeof value.$ref === "string")) return value;
        let name;
        do { name = `afterburner_schema_${++index}`; } while (Object.hasOwn(root.$defs ?? {}, name));
        const target = `#/$defs/${name}`;
        definitions[name] = value;
        moves.push([path, target]);
        return { $ref: target };
    }
    flatten(root, "#");
    // Local JSON pointers must follow relocated schemas, including pointers into their children.
    moves.sort(([left], [right]) => right.length - left.length);
    function remap(value, path) {
        if (!object(value)) return value;
        for (const keyword of ["$ref", "$dynamicRef", "$recursiveRef"]) {
            const reference = value[keyword];
            if (typeof reference !== "string" || !reference.startsWith("#/")) continue;
            const decoded = decodeURIComponent(reference);
            const move = moves.find(([source]) => decoded === source || decoded.startsWith(`${source}/`));
            if (move) value[keyword] = move[1] + decoded.slice(move[0].length);
        }
        visitChildren(value, remap, path);
        return value;
    }
    remap(root, "#");
    for (const value of Object.values(definitions)) remap(value, "#");
    root.$defs = { ...root.$defs, ...definitions };
    return root;
}

export function rewriteLegacyTools(payload) {
    const names = new Set();
    function expand(tools) {
        return tools.flatMap(tool => {
            if (tool.type === "tool_search") return [];
            if (tool.type === "namespace") {
                if (!Array.isArray(tool.tools)) throw new Error("legacyTools requires namespace.tools.");
                return expand(tool.tools);
            }
            if (tool.type !== "function") return [tool];
            if (names.has(tool.name)) {
                throw new Error(`legacyTools cannot flatten duplicate function name '${tool.name}'.`);
            }
            names.add(tool.name);
            const result = { ...tool, parameters: flattenToolSchema(tool.parameters) };
            delete result.defer_loading;
            return [result];
        });
    }
    if (Array.isArray(payload.tools)) payload.tools = expand(payload.tools);
    if (Array.isArray(payload.input)) {
        payload.input = payload.input.filter(item =>
            item.type !== "tool_search_call" && item.type !== "tool_search_output");
        for (const item of payload.input) {
            if (item.type === "function_call") delete item.namespace;
        }
    }
    if (object(payload.tool_choice)) {
        if (payload.tool_choice.type === "tool_search") {
            throw new Error("legacyTools does not support forcing a native tool_search call.");
        }
        if (payload.tool_choice.type === "function") delete payload.tool_choice.namespace;
        if (Array.isArray(payload.tool_choice.tools)) {
            payload.tool_choice.tools = payload.tool_choice.tools.map(tool => {
                if (tool.type !== "function") {
                    throw new Error("legacyTools only supports function entries in allowed tool choices.");
                }
                const result = { ...tool };
                delete result.namespace;
                return result;
            });
        }
    }
    if (depth(payload) > 16) {
        throw new Error("legacyTools request still exceeds the upstream JSON depth limit of 16 outside relocatable tool schemas.");
    }
}
