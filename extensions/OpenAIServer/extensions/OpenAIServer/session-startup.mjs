function errorText(error) {
    return String(error?.message ?? error ?? "unknown error").replace(/[\r\n\t]+/g, " ").slice(0, 500);
}

export async function registerThenInitialize({ joinSession, registration, initialize }) {
    const session = await joinSession(registration);
    let disposed = false;
    const ready = Promise.resolve()
        .then(() => initialize(session))
        .catch(async error => {
            if (!disposed) {
                await session?.log?.(`OpenAI Server startup failed: ${errorText(error)}.`);
            }
            return { ok: false, error };
        });
    return {
        session,
        ready,
        dispose() {
            disposed = true;
        }
    };
}
