package tooling

import (
	"fmt"
	"strings"

	"github.com/nbaertsch/afterburner/internal/ui/capability"
	"github.com/nbaertsch/afterburner/internal/ui/component"
	"github.com/nbaertsch/afterburner/internal/ui/protocol"
	"github.com/nbaertsch/afterburner/internal/ui/surface"
)

type PublicCatalog struct {
	Protocol     protocol.Revision        `json:"protocol"`
	Components   []component.CatalogEntry `json:"components"`
	Surfaces     []surface.CatalogEntry   `json:"surfaces"`
	Capabilities []capability.Descriptor  `json:"capabilities"`
}

func Catalog() PublicCatalog {
	return PublicCatalog{
		Protocol:     protocol.CurrentRevision(),
		Components:   component.PublicCatalog(),
		Surfaces:     surface.PublicCatalog(),
		Capabilities: capability.CoreDescriptors(),
	}
}

func FormatCatalog(catalog PublicCatalog) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "Afterburner UI %s revision %d\n", catalog.Protocol.Protocol, catalog.Protocol.Revision)
	writeCatalogSection(&builder, "Components", len(catalog.Components), func() {
		for _, entry := range catalog.Components {
			fmt.Fprintf(&builder, "  %-18s %-12s %s\n", entry.Kind, entry.Stability, entry.Description)
		}
	})
	writeCatalogSection(&builder, "Surfaces", len(catalog.Surfaces), func() {
		for _, entry := range catalog.Surfaces {
			fmt.Fprintf(&builder, "  %-18s %-12s %s\n", entry.Kind, entry.Stability, entry.Description)
		}
	})
	writeCatalogSection(&builder, "Capabilities", len(catalog.Capabilities), func() {
		for _, entry := range catalog.Capabilities {
			fmt.Fprintf(&builder, "  %-36s %-12s %s\n", entry.ID, entry.Stability, entry.Description)
		}
	})
	return builder.String()
}

func writeCatalogSection(builder *strings.Builder, title string, count int, writeEntries func()) {
	fmt.Fprintf(builder, "\n%s (%d)\n", title, count)
	writeEntries()
}
