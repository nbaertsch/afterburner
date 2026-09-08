export async function activate(api) {
  const { components: ui } = api.ui;
  let state = { name: "", enabled: true };
  let handle;

  const frame = () => ({
    title: "Example settings",
    document: api.ui.createDocument(ui.dialog({ title: "Example settings" }, [
      ui.form({ title: "Configuration" }, [
        ui.textInput({ label: "Display name", value: state.name }, [], { id: "display-name" }),
        ui.checkbox({ label: "Enabled", checked: state.enabled }, [], { id: "enabled" }),
        ui.button({ label: "Save" }, [], {
          id: "save",
          actionBindings: { activate: "save" }
        })
      ], { id: "settings-form" })
    ], { id: "settings-root" }), {
      surfaceId: "settings"
    })
  });

  handle = api.ui.registerSurface({
    id: "settings",
    kind: "modal",
    actions: [{
      name: "save",
      label: "Save",
      handler: async () => ({ ...state })
    }],
    open: frame,
    onEvent: async (event, controls) => {
      if (event.type !== "change") return;
      if (event.targetId === "display-name") state.name = String(event.value ?? "");
      if (event.targetId === "enabled") state.enabled = event.value === true;
      await controls.update(frame());
    }
  });

  return () => handle.dispose();
}
