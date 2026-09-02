const tasks = new Map([
    ["sample", { id: "sample", title: "External sample task", state: "running", progress: 25 }]
]);
const listeners = new Set();

export async function activate(api) {
    api.registerExternalTaskProvider({
            id: "fake",
            snapshot: async () => [...tasks.values()],
            read: async (id) => tasks.get(id) ?? null,
            write: async (id, message) => ({ id, accepted: true, message }),
            cancel: async (id) => {
                const task = tasks.get(id);
                if (!task) return false;
                tasks.set(id, { ...task, state: "cancelled", progress: 100 });
                for (const listener of listeners) listener({ type: "changed", id });
                return true;
            },
            subscribe: (listener) => {
                listeners.add(listener);
                return () => listeners.delete(listener);
            }
    });
}
