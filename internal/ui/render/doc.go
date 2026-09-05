// Package render translates versioned UI component trees into deterministic terminal frames.
//
// The package is deliberately adapter-only: it may use Bubble Tea models,
// Bubbles widgets, and Lip Gloss styles to compose strings, but it never starts
// a tea.Program, opens stdin/stdout, switches alternate screen, or changes raw
// terminal mode. The Afterburner terminal adapter remains the sole terminal
// owner and decides where rendered frames are written.
package render
