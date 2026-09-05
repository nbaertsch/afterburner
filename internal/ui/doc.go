// Package ui documents the Afterburner enterprise UI contract boundary.
//
// The afterburner.ui contract is intentionally separated from the terminal
// broker, runtime package, and built-in extensions. Packages under internal/ui
// define only versioned data contracts and implementation interfaces. Host,
// renderer, extension SDK, reconciler, policy, audit, and observability work can
// proceed independently by depending on these contracts without importing each
// other's runtime code.
//
// Contract decisions for implementers:
//   - protocol.Protocol and protocol.ProtocolRevision identify every wire
//     envelope, schema, capability, audit, and compatibility record.
//   - Black Box is modeled as an optional observability event sink extension;
//     renderers and hosts must continue operating when that sink is absent.
//   - Extension manifests remain schemaVersion 1. The optional ui property is
//     additive and must not invalidate existing manifests that omit it.
//   - Component kinds, semantic tokens, error codes, SLO IDs, and capability IDs
//     are public compatibility surfaces; change them only by adding identifiers
//     or by introducing a new protocol revision.
package ui
