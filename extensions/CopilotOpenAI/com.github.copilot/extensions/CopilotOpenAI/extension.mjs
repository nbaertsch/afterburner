import { activate as activateExtension } from "../../../extensions/CopilotOpenAI/extension.mjs";

export const instance = await activateExtension();

export async function activate(api) {
    return activateExtension(api);
}
