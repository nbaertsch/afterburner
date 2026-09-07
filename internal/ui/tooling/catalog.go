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

func emptyCatalog(protocol protocol.Revision) PublicCatalog {
	return PublicCatalog{
		Protocol:     protocol,
		Components:   []component.CatalogEntry{},
		Surfaces:     []surface.CatalogEntry{},
		Capabilities: []capability.Descriptor{},
	}
}

func FilterCatalog(catalog PublicCatalog, section string) (PublicCatalog, error) {
	section = strings.ToLower(strings.TrimSpace(section))
	if section == "" || section == "all" {
		return catalog, nil
	}
	filtered := emptyCatalog(catalog.Protocol)
	switch section {
	case "component", "components":
		filtered.Components = catalog.Components
	case "surface", "surfaces":
		filtered.Surfaces = catalog.Surfaces
	case "capability", "capabilities":
		filtered.Capabilities = catalog.Capabilities
	default:
		return PublicCatalog{}, fmt.Errorf("unknown ui catalog section %q", section)
	}
	return filtered, nil
}

func SearchCatalog(catalog PublicCatalog, query string) PublicCatalog {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return catalog
	}
	filtered := emptyCatalog(catalog.Protocol)
	for _, entry := range catalog.Components {
		if catalogTextMatches(query, string(entry.Kind), string(entry.Stability), entry.Description) {
			filtered.Components = append(filtered.Components, entry)
		}
	}
	for _, entry := range catalog.Surfaces {
		if catalogTextMatches(query, string(entry.Kind), string(entry.Stability), entry.Description) {
			filtered.Surfaces = append(filtered.Surfaces, entry)
		}
	}
	for _, entry := range catalog.Capabilities {
		if catalogTextMatches(query, string(entry.ID), string(entry.Stability), entry.Description) {
			filtered.Capabilities = append(filtered.Capabilities, entry)
		}
	}
	return filtered
}

func catalogTextMatches(query string, values ...string) bool {
	for _, value := range values {
		if strings.Contains(strings.ToLower(value), query) {
			return true
		}
	}
	return false
}

func CatalogEmpty(catalog PublicCatalog) bool {
	return len(catalog.Components) == 0 && len(catalog.Surfaces) == 0 && len(catalog.Capabilities) == 0
}

func FormatCatalog(catalog PublicCatalog) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "Afterburner UI %s revision %d\n", catalog.Protocol.Protocol, catalog.Protocol.Revision)
	if len(catalog.Components) > 0 {
		writeCatalogSection(&builder, "Components", len(catalog.Components), func() {
			for _, entry := range catalog.Components {
				fmt.Fprintf(&builder, "  %-18s %-12s %s\n", entry.Kind, entry.Stability, entry.Description)
			}
		})
	}
	if len(catalog.Surfaces) > 0 {
		writeCatalogSection(&builder, "Surfaces", len(catalog.Surfaces), func() {
			for _, entry := range catalog.Surfaces {
				fmt.Fprintf(&builder, "  %-18s %-12s %s\n", entry.Kind, entry.Stability, entry.Description)
			}
		})
	}
	if len(catalog.Capabilities) > 0 {
		writeCatalogSection(&builder, "Capabilities", len(catalog.Capabilities), func() {
			for _, entry := range catalog.Capabilities {
				fmt.Fprintf(&builder, "  %-36s %-12s %s\n", entry.ID, entry.Stability, entry.Description)
			}
		})
	}
	return builder.String()
}

func writeCatalogSection(builder *strings.Builder, title string, count int, writeEntries func()) {
	fmt.Fprintf(builder, "\n%s (%d)\n", title, count)
	writeEntries()
}
