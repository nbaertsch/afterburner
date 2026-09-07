# Enterprise UI administrator guide

Afterburner exposes UI diagnostics and policy operations under `afterburn ui ...`.

## Diagnostics

```powershell
afterburn ui doctor --json
afterburn ui catalog
afterburn ui catalog capabilities --json
afterburn ui inspect --json black-box
afterburn ui trace --extension black-box --redacted --json
```

Catalog output is generated from the same public component, surface, and capability registries that manifest and grant validation enforce. Use it during reviews to catch unsupported surface kinds, component declarations, or capability grants before installation. Trace output is deterministic and metadata-first. When Black Box is installed, the viewer reads its metadata-only JSONL records from `extension-data\black-box` and keeps prompt, response, source, token, and tool body fields redacted.

## Policy and grants

Runtime UI grants are stored in `~\.afterburner\config\ui-grants.json`.

```powershell
afterburn ui grant sample-ui ui.action.invoke sample-panel/*
afterburn ui revoke sample-ui action-invoke:sample-panel-all
afterburn ui policy show
afterburn ui policy set .\ui-enterprise-policy.json
```

Enterprise policy files use `schemas\ui-enterprise-policy-v1.schema.json`. Use deny-by-default, signed built-in requirements, registry epoch minimums, and revoked signer constraints for sensitive capabilities.

## Certification

Certification emits human and machine-readable reports covering component support, keyboard/accessibility, fallback, security/grants, protocol compatibility, quotas, recovery, observability, and native metadata requirements.

```powershell
afterburn ui certify --extension sample-ui --surface sample-panel
afterburn ui certify --extension sample-ui --surface sample-panel --json
```

The certification runner does not launch extension runtime code and does not create an observability dashboard. It consumes only public UI contracts and optional Black Box metadata records.
