package extensions

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/nbaertsch/afterburner/internal/assets"
	"github.com/nbaertsch/afterburner/internal/home"
	"github.com/nbaertsch/afterburner/internal/registry"
	"github.com/nbaertsch/afterburner/internal/sessions"
)

var validID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
var validCapability = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

const packageCacheRetentionGrace = 24 * time.Hour

type Manager struct {
	Layout         home.Layout
	Stdout         io.Writer
	CoreVersion    string
	CopilotVersion string
	// BuiltinFetcher resolves built-ins from an authenticated release source.
	// Production built-ins are never embedded in the core executable.
	BuiltinFetcher BuiltinFetcher
}

type FetchedBuiltin struct {
	Path    string
	Source  registry.Source
	Cleanup func()
}

// BuiltinFetcher resolves a built-in extension ID to an extracted, verified
// source directory. Implementations are responsible for any network fetch
// and signature verification; a successful result is treated as a signed
// release built-in and bound to its source and package hashes. FetchBuiltin
// should return an error for any ID it cannot resolve.
type BuiltinFetcher interface {
	FetchBuiltin(id string) (FetchedBuiltin, error)
}

func (m Manager) InstallBuiltins(ids []string) error {
	return m.syncBuiltins(ids)
}

func (m Manager) SyncBuiltins(ids []string) error {
	return m.syncBuiltins(ids)
}

func (m Manager) syncBuiltins(ids []string) error {
	return m.withRegistry(func(value *registry.Registry) error {
		catalog := assets.Builtins()
		if len(ids) == 0 {
			for id := range catalog {
				ids = append(ids, id)
			}
			sort.Strings(ids)
		}
		normalized, err := normalizeIDs(ids)
		if err != nil {
			return err
		}
		ids = normalized
		if slices.Contains(ids, registry.OpenAIServerID) {
			registry.MigrateOpenAIServerAlias(value)
		}
		for _, id := range ids {
			_, knownBuiltin := catalog[id]
			if !knownBuiltin {
				return fmt.Errorf("unknown built-in extension %q; available: %s", id, strings.Join(sortedKeys(catalog), ", "))
			}
			if m.BuiltinFetcher == nil {
				return fmt.Errorf("built-in extension %q requires a signed release source", id)
			}
			fetched, err := m.BuiltinFetcher.FetchBuiltin(id)
			if err != nil {
				return fmt.Errorf("fetch built-in extension %q: %w", id, err)
			}
			if fetched.Cleanup == nil {
				fetched.Cleanup = func() {}
			}
			source := fetched.Path
			cleanup := fetched.Cleanup
			if fetched.Source.Type != "signed-release" || fetched.Source.Value != id ||
				fetched.Source.Version == "" || fetched.Source.Commit == "" ||
				fetched.Source.Digest == "" || fetched.Source.ManifestDigest == "" ||
				fetched.Source.SignerFingerprint == "" {
				cleanup()
				return fmt.Errorf("fetched built-in extension %q has incomplete signed provenance", id)
			}
			manifest, err := readManifest(source)
			if err != nil || manifest.ID != id || manifest.Visibility != "builtin" {
				cleanup()
				return fmt.Errorf("fetched built-in extension %q has an invalid or mismatched manifest", id)
			}
			sourceMetadata := fetched.Source
			previous := value.Extensions[id]
			enabled := builtinDefaultEnabled(id)
			if previous.Manifest.ID == id {
				enabled = previous.Enabled
			}
			entry, err := m.materialize(source, sourceMetadata, previous, enabled, true, false)
			if err != nil {
				cleanup()
				return err
			}
			value.Extensions[id] = entry
			if previous.ActivePath == entry.ActivePath {
				fmt.Fprintf(m.Stdout, "Built-in extension %q is already synced at %s.\n", id, filepath.Base(entry.ActivePath))
			} else {
				fmt.Fprintf(m.Stdout, "Synced built-in extension %q to %s.\n", id, filepath.Base(entry.ActivePath))
			}
			if err := m.initializeBuiltinConfig(id, source); err != nil {
				cleanup()
				return err
			}
			cleanup()
		}
		return nil
	})
}

func (m Manager) SetBuiltinsEnabled(ids []string, enabled bool) error {
	if len(ids) == 0 {
		return fmt.Errorf("at least one built-in extension ID is required")
	}
	return m.withRegistry(func(value *registry.Registry) error {
		normalized, err := normalizeIDs(ids)
		if err != nil {
			return err
		}
		for _, id := range normalized {
			if id == registry.OpenAIServerID {
				if _, canonicalInstalled := value.Extensions[registry.OpenAIServerID]; canonicalInstalled {
					registry.MigrateOpenAIServerAlias(value)
				}
			}
			key := registryEntryKey(value, id)
			entry, ok := value.Extensions[key]
			legacyOpenAIServer := id == registry.OpenAIServerID && key == registry.LegacyOpenAIServerID
			if !ok || (entry.Manifest.Visibility != "builtin" && !legacyOpenAIServer) {
				return fmt.Errorf("built-in extension %q is not installed; run 'afterburn install %s'", id, id)
			}
			entry.Enabled = enabled
			if !entry.Identity.IsZero() {
				sealed, err := registry.SealEntry(m.Layout.Root, entry)
				if err != nil {
					return err
				}
				entry = sealed
			}
			value.Extensions[key] = entry
			fmt.Fprintf(m.Stdout, "%s built-in extension %q.\n", map[bool]string{true: "Enabled", false: "Disabled"}[enabled], id)
		}
		return nil
	})
}

func (m Manager) UninstallBuiltins(ids []string) error {
	if len(ids) == 0 {
		return fmt.Errorf("at least one built-in extension ID is required")
	}
	var remove []string
	err := m.withRegistry(func(value *registry.Registry) error {
		normalized, err := normalizeIDs(ids)
		if err != nil {
			return err
		}
		for _, id := range normalized {
			if id == registry.OpenAIServerID {
				found := false
				for _, key := range []string{registry.OpenAIServerID, registry.LegacyOpenAIServerID} {
					entry, ok := value.Extensions[key]
					if !ok {
						continue
					}
					if key == registry.OpenAIServerID && entry.Manifest.Visibility != "builtin" {
						return fmt.Errorf("extension %q is not a built-in", id)
					}
					found = true
					delete(value.Extensions, key)
					remove = append(remove, filepath.Join(m.Layout.Extensions, key))
				}
				if !found {
					fmt.Fprintf(m.Stdout, "Uninstalled built-in extension %q.\n", id)
					continue
				}
				fmt.Fprintf(m.Stdout, "Uninstalled built-in extension %q.\n", id)
				continue
			}
			key := registryEntryKey(value, id)
			entry, ok := value.Extensions[key]
			if ok && entry.Manifest.Visibility != "builtin" {
				return fmt.Errorf("extension %q is not a built-in", id)
			}
			delete(value.Extensions, key)
			remove = append(remove, filepath.Join(m.Layout.Extensions, key))
			fmt.Fprintf(m.Stdout, "Uninstalled built-in extension %q.\n", id)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, path := range remove {
		if !registry.Within(path, m.Layout.Extensions) {
			return fmt.Errorf("refusing to remove package cache outside managed root: %s", path)
		}
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("remove package cache %s: %w", path, err)
		}
	}
	return nil
}

func (m Manager) Install(source string) error {
	resolvedPath, sourceMetadata, cleanup, err := m.resolveSource(source)
	if err != nil {
		return err
	}
	defer cleanup()
	return m.withRegistry(func(value *registry.Registry) error {
		entry, err := m.materialize(resolvedPath, sourceMetadata, registry.Entry{}, false, false, false)
		if err != nil {
			return err
		}
		value.Extensions[entry.Manifest.ID] = entry
		fmt.Fprintf(m.Stdout, "Installed Afterburner extension %q disabled at %s.\n", entry.Manifest.ID, filepath.Base(entry.ActivePath))
		return nil
	})
}

func (m Manager) SetEnabled(id string, enabled bool) error {
	id = canonicalID(id)
	return m.withRegistry(func(value *registry.Registry) error {
		if id == registry.OpenAIServerID {
			if _, canonicalInstalled := value.Extensions[registry.OpenAIServerID]; canonicalInstalled {
				registry.MigrateOpenAIServerAlias(value)
			}
		}
		key := registryEntryKey(value, id)
		entry, ok := value.Extensions[key]
		if !ok {
			return fmt.Errorf("unknown Afterburner extension %q", id)
		}
		if enabled {
			if err := ValidateCompatibility(entry.Manifest, m.CoreVersion, m.CopilotVersion); err != nil {
				return err
			}
		}
		entry.Enabled = enabled
		if !entry.Identity.IsZero() {
			sealed, err := registry.SealEntry(m.Layout.Root, entry)
			if err != nil {
				return err
			}
			entry = sealed
		}
		value.Extensions[key] = entry
		fmt.Fprintf(m.Stdout, "%s %q.\n", map[bool]string{true: "Enabled", false: "Disabled"}[enabled], id)
		return nil
	})
}

func (m Manager) Update(id string) error {
	id = canonicalID(id)
	return m.withRegistry(func(value *registry.Registry) error {
		key := registryEntryKey(value, id)
		current, ok := value.Extensions[key]
		if !ok {
			return fmt.Errorf("unknown Afterburner extension %q", id)
		}
		if current.Source.Type != "path" && current.Source.Type != "git" && current.Source.Type != "archive" {
			return fmt.Errorf("extension %q does not have an updateable source", id)
		}
		resolvedPath, sourceMetadata, cleanup, err := m.resolveRegistrySource(current.Source)
		if err != nil {
			return err
		}
		defer cleanup()
		legacyOpenAIServer := id == registry.OpenAIServerID && key == registry.LegacyOpenAIServerID
		entry, err := m.materialize(resolvedPath, sourceMetadata, current, current.Enabled, false, legacyOpenAIServer)
		if err != nil {
			return err
		}
		if entry.Manifest.ID != key {
			return fmt.Errorf("source package changed identity from %q to %q", key, entry.Manifest.ID)
		}
		value.Extensions[key] = entry
		if entry.ActivePath == current.ActivePath {
			fmt.Fprintf(m.Stdout, "%q is already current.\n", id)
		} else {
			fmt.Fprintf(m.Stdout, "Updated %q to %s.\n", id, filepath.Base(entry.ActivePath))
		}
		return nil
	})
}

func (m Manager) UpdateAll() error {
	value, err := registry.Load(m.Layout.Root)
	if err != nil {
		return err
	}
	var builtins []string
	for _, id := range sortedKeys(value.Extensions) {
		switch value.Extensions[id].Source.Type {
		case "path", "git", "archive":
			if err := m.Update(id); err != nil {
				return err
			}
		case "signed-release":
			builtins = append(builtins, id)
		}
	}
	if len(builtins) > 0 {
		if m.BuiltinFetcher == nil {
			fmt.Fprintf(m.Stdout, "Built-in extensions (%s) are pinned to the installed Afterburner core release; run `afterburn update` to sync them.\n", strings.Join(builtins, ", "))
			return nil
		}
		return m.SyncBuiltins(builtins)
	}
	return nil
}

func (m Manager) Rollback(id string) error {
	id = canonicalID(id)
	return m.withRegistry(func(value *registry.Registry) error {
		key := registryEntryKey(value, id)
		entry, ok := value.Extensions[key]
		if !ok {
			return fmt.Errorf("unknown Afterburner extension %q", id)
		}
		if entry.PreviousActivePath == nil || *entry.PreviousActivePath == "" {
			return fmt.Errorf("no rollback version is available for %q", id)
		}
		if entry.PreviousPackage == nil {
			return fmt.Errorf("rollback package for %q has no verified package identity; update or reinstall it before rollback", id)
		}
		previousPackage := *entry.PreviousPackage
		previous := previousPackage.ActivePath
		if previous == "" || previous != *entry.PreviousActivePath {
			return fmt.Errorf("rollback package identity mismatch for %q", id)
		}
		legacyOpenAIServerRollback := id == registry.OpenAIServerID &&
			registry.Within(previous, filepath.Join(m.Layout.Extensions, registry.LegacyOpenAIServerID))
		if !registry.Within(previous, filepath.Join(m.Layout.Extensions, id)) && !legacyOpenAIServerRollback {
			return fmt.Errorf("rollback package for %q escapes its managed root", id)
		}
		manifest, err := readManifestWithCanonicalID(previous, !legacyOpenAIServerRollback)
		if err != nil {
			return fmt.Errorf("validate rollback package for %q: %w", id, err)
		}
		if manifest.ID != id && !(legacyOpenAIServerRollback && manifest.ID == registry.LegacyOpenAIServerID) {
			return fmt.Errorf("rollback package identity mismatch for %q", id)
		}
		if err := ValidateCompatibility(manifest, m.CoreVersion, m.CopilotVersion); err != nil {
			return err
		}
		manifestHash, err := registry.HashFile(filepath.Join(previous, "afterburner.json"))
		if err != nil {
			return fmt.Errorf("hash rollback manifest for %q: %w", id, err)
		}
		treeHash, err := registry.HashTree(previous)
		if err != nil {
			return fmt.Errorf("hash rollback package for %q: %w", id, err)
		}
		current := entry.ActivePath
		currentSource := entry.Source
		currentPackage := packageReference(entry)
		previousSource := previousPackage.Source
		if previousPackage.ManifestHash != "sha256:"+manifestHash ||
			previousPackage.TreeHash != "sha256:"+treeHash {
			return fmt.Errorf("rollback package for %q changed after installation", id)
		}
		if previousPackage.BuiltinSigned {
			if manifest.Visibility != "builtin" || !registry.IsTrustedBuiltinSourceType(previousSource.Type) ||
				previousSource.Value != manifest.ID || previousPackage.SignerID != "afterburner-release" ||
				previousPackage.SignerFingerprint == "" ||
				previousPackage.SignerFingerprint != previousSource.SignerFingerprint {
				return fmt.Errorf("rollback package for %q has invalid signed provenance", id)
			}
		}
		entry.ActivePath = previous
		entry.PreviousActivePath = &current
		entry.PreviousSource = &currentSource
		entry.PreviousPackage = currentPackage
		entry.Manifest = manifest
		entry.Source = previousSource
		entry.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		trustedBuiltin := manifest.Visibility == "builtin" && registry.IsTrustedBuiltinSourceType(entry.Source.Type) && entry.Source.Value == manifest.ID
		entry.Identity = identityBinding(manifest, entry.Source, manifestHash, treeHash, entry.Identity.GrantEpoch, entry.UpdatedAt, trustedBuiltin)
		sealed, err := registry.SealEntry(m.Layout.Root, entry)
		if err != nil {
			return err
		}
		entry = sealed
		delete(value.Extensions, key)
		value.Extensions[manifest.ID] = entry
		fmt.Fprintf(m.Stdout, "Rolled back %q to %s.\n", id, filepath.Base(previous))
		return nil
	})
}

func (m Manager) List() error {
	value, err := registry.Load(m.Layout.Root)
	if err != nil {
		return err
	}
	for _, id := range sortedKeys(value.Extensions) {
		entry := value.Extensions[id]
		state := "disabled"
		if entry.Enabled {
			state = "enabled "
		}
		fmt.Fprintf(m.Stdout, "%s %s (%s) %s\n", state, id, entry.Manifest.Visibility, filepath.Base(entry.ActivePath))
	}
	return nil
}

func (m Manager) Inspect(id string) error {
	value, err := registry.Load(m.Layout.Root)
	if err != nil {
		return err
	}
	entry, ok := value.Extensions[registryEntryKey(&value, canonicalID(id))]
	if !ok {
		return fmt.Errorf("unknown Afterburner extension %q", id)
	}
	encoded, _ := json.MarshalIndent(entry, "", "  ")
	fmt.Fprintln(m.Stdout, string(encoded))
	return nil
}

func (m Manager) withRegistry(update func(*registry.Registry) error) error {
	if err := os.MkdirAll(m.Layout.Root, 0o700); err != nil {
		return fmt.Errorf("create Afterburner home: %w", err)
	}
	release, err := acquireLock(filepath.Join(m.Layout.Root, ".registry.lock"))
	if err != nil {
		return err
	}
	defer release()
	value, err := registry.Load(m.Layout.Root)
	if err != nil {
		return err
	}
	packageSnapshot, err := snapshotPackageCache(m.Layout)
	if err != nil {
		return err
	}
	if err := update(&value); err != nil {
		if cleanupErr := removeNewPackageCacheEntries(m.Layout, packageSnapshot); cleanupErr != nil {
			return fmt.Errorf("%w; additionally failed to clean package cache: %v", err, cleanupErr)
		}
		return err
	}
	if value.Epoch == 0 {
		value.Epoch = 1
	}
	value.Epoch++
	if err := registry.Save(m.Layout.Root, value); err != nil {
		if cleanupErr := removeNewPackageCacheEntries(m.Layout, packageSnapshot); cleanupErr != nil {
			return fmt.Errorf("%w; additionally failed to clean package cache: %v", err, cleanupErr)
		}
		return err
	}
	if err := sessions.Reconcile(m.Layout, value); err != nil {
		return err
	}
	return prunePackageCache(m.Layout, value, time.Now().UTC().Add(-packageCacheRetentionGrace))
}

func snapshotPackageCache(layout home.Layout) (map[string]struct{}, error) {
	snapshot := map[string]struct{}{}
	extensionRoots, err := os.ReadDir(layout.Extensions)
	if os.IsNotExist(err) {
		return snapshot, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read extension package cache: %w", err)
	}
	for _, extensionRoot := range extensionRoots {
		if !extensionRoot.IsDir() {
			continue
		}
		root := filepath.Join(layout.Extensions, extensionRoot.Name())
		packages, err := os.ReadDir(root)
		if err != nil {
			return nil, fmt.Errorf("read extension package cache %s: %w", root, err)
		}
		for _, packageEntry := range packages {
			if packageEntry.IsDir() && !strings.HasPrefix(packageEntry.Name(), ".staging-") {
				snapshot[cleanPathKey(filepath.Join(root, packageEntry.Name()))] = struct{}{}
			}
		}
	}
	return snapshot, nil
}

func removeNewPackageCacheEntries(layout home.Layout, snapshot map[string]struct{}) error {
	current, err := snapshotPackageCache(layout)
	if err != nil {
		return err
	}
	for path := range current {
		if _, existed := snapshot[path]; existed {
			continue
		}
		if !registry.Within(path, layout.Extensions) {
			return fmt.Errorf("refusing to clean package cache outside managed root: %s", path)
		}
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("clean package cache %s: %w", path, err)
		}
	}
	return nil
}

func prunePackageCache(layout home.Layout, value registry.Registry, cutoff time.Time) error {
	keep := make(map[string]struct{}, len(value.Extensions)*2)
	for _, entry := range value.Extensions {
		if entry.ActivePath != "" {
			keep[cleanPathKey(entry.ActivePath)] = struct{}{}
		}
		if entry.PreviousPackage != nil && entry.PreviousPackage.ActivePath != "" {
			keep[cleanPathKey(entry.PreviousPackage.ActivePath)] = struct{}{}
		}
	}
	extensionRoots, err := os.ReadDir(layout.Extensions)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read extension package cache: %w", err)
	}
	for _, extensionRoot := range extensionRoots {
		if !extensionRoot.IsDir() {
			continue
		}
		root := filepath.Join(layout.Extensions, extensionRoot.Name())
		packages, err := os.ReadDir(root)
		if err != nil {
			return fmt.Errorf("read extension package cache %s: %w", root, err)
		}
		for _, packageEntry := range packages {
			if !packageEntry.IsDir() || strings.HasPrefix(packageEntry.Name(), ".staging-") {
				continue
			}
			path := filepath.Join(root, packageEntry.Name())
			if _, retained := keep[cleanPathKey(path)]; retained {
				continue
			}
			info, err := packageEntry.Info()
			if err != nil {
				return fmt.Errorf("inspect extension package cache %s: %w", path, err)
			}
			if info.ModTime().After(cutoff) {
				continue
			}
			if !registry.Within(path, layout.Extensions) {
				return fmt.Errorf("refusing to prune package cache outside managed root: %s", path)
			}
			if err := os.RemoveAll(path); err != nil {
				return fmt.Errorf("prune extension package cache %s: %w", path, err)
			}
		}
	}
	return nil
}

func cleanPathKey(path string) string {
	return strings.ToLower(filepath.Clean(path))
}

func (m Manager) materialize(source string, sourceMetadata registry.Source, previous registry.Entry, enabled bool, trustedBuiltin bool, preserveLegacyID bool) (registry.Entry, error) {
	source, err := filepath.Abs(source)
	if err != nil {
		return registry.Entry{}, err
	}
	manifest, err := readManifestWithCanonicalID(source, !preserveLegacyID)
	if err != nil {
		return registry.Entry{}, err
	}
	if err := ValidateCompatibility(manifest, m.CoreVersion, m.CopilotVersion); err != nil {
		return registry.Entry{}, err
	}
	if registry.IsReservedBuiltinID(manifest.ID) && !trustedBuiltin {
		return registry.Entry{}, fmt.Errorf("extension %q uses a reserved built-in ID and must come from a verified built-in source", manifest.ID)
	}
	if manifest.Visibility == "builtin" {
		if !trustedBuiltin || !registry.IsTrustedBuiltinSourceType(sourceMetadata.Type) || sourceMetadata.Value != manifest.ID {
			return registry.Entry{}, fmt.Errorf("extension %q declares built-in visibility but its source is not a verified built-in", manifest.ID)
		}
	} else if trustedBuiltin || registry.IsTrustedBuiltinSourceType(sourceMetadata.Type) {
		return registry.Entry{}, fmt.Errorf("verified built-in source %q must declare built-in visibility", manifest.ID)
	}
	extensionRoot := filepath.Join(m.Layout.Extensions, manifest.ID)
	if !registry.Within(extensionRoot, m.Layout.Extensions) {
		return registry.Entry{}, fmt.Errorf("extension target escapes managed root")
	}
	if err := os.MkdirAll(extensionRoot, 0o700); err != nil {
		return registry.Entry{}, err
	}
	staging, err := os.MkdirTemp(extensionRoot, ".staging-")
	if err != nil {
		return registry.Entry{}, err
	}
	defer os.RemoveAll(staging)
	if err := copyTree(source, staging); err != nil {
		return registry.Entry{}, err
	}
	treeHash, err := registry.HashTree(staging)
	if err != nil {
		return registry.Entry{}, err
	}
	manifestHash, err := registry.HashFile(filepath.Join(staging, "afterburner.json"))
	if err != nil {
		return registry.Entry{}, err
	}
	version := "local-" + treeHash[:16]
	target := filepath.Join(extensionRoot, version)
	if _, err := os.Stat(target); os.IsNotExist(err) {
		if err := os.Rename(staging, target); err != nil {
			if _, statErr := os.Stat(target); statErr != nil {
				return registry.Entry{}, err
			}
		}
	} else if err != nil {
		return registry.Entry{}, err
	}
	existingTreeHash, err := registry.HashTree(target)
	if err != nil {
		return registry.Entry{}, err
	}
	existingManifestHash, err := registry.HashFile(filepath.Join(target, "afterburner.json"))
	if err != nil {
		return registry.Entry{}, err
	}
	if existingTreeHash != treeHash || existingManifestHash != manifestHash {
		return registry.Entry{}, fmt.Errorf("installed extension package %q changed after installation", manifest.ID)
	}
	var previousPath *string
	var previousSource *registry.Source
	var previousPackage *registry.PackageReference
	if previous.ActivePath != "" && previous.ActivePath != target {
		value := previous.ActivePath
		previousPath = &value
		source := previous.Source
		previousSource = &source
		previousPackage = packageReference(previous)
	} else {
		previousPath = previous.PreviousActivePath
		previousSource = previous.PreviousSource
		previousPackage = previous.PreviousPackage
	}
	if sourceMetadata.Version == "" {
		sourceMetadata.Version = version
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	identity := identityBinding(manifest, sourceMetadata, manifestHash, treeHash, previous.Identity.GrantEpoch, now, trustedBuiltin)
	entry := registry.Entry{
		Enabled:            enabled,
		ActivePath:         target,
		PreviousActivePath: previousPath,
		PreviousSource:     previousSource,
		PreviousPackage:    previousPackage,
		Manifest:           manifest,
		Source:             sourceMetadata,
		Identity:           identity,
		UpdatedAt:          now,
	}
	return registry.SealEntry(m.Layout.Root, entry)
}

func packageReference(entry registry.Entry) *registry.PackageReference {
	if entry.ActivePath == "" || entry.Identity.ManifestHash == "" || entry.Identity.TreeHash == "" {
		return nil
	}
	return &registry.PackageReference{
		ActivePath:        entry.ActivePath,
		ManifestHash:      entry.Identity.ManifestHash,
		TreeHash:          entry.Identity.TreeHash,
		Source:            entry.Source,
		BuiltinSigned:     entry.Identity.BuiltinSigned,
		SignerID:          entry.Identity.SignerID,
		SignerFingerprint: entry.Identity.SignerFingerprint,
	}
}

func (m Manager) resolveSource(spec string) (string, registry.Source, func(), error) {
	if spec == "" {
		return "", registry.Source{}, nil, fmt.Errorf("an extension source is required")
	}
	if local, err := filepath.Abs(spec); err == nil {
		if info, statErr := os.Stat(local); statErr == nil && info.Mode().IsRegular() &&
			strings.EqualFold(filepath.Ext(local), ".zip") {
			return m.extractExternalArchive(local)
		}
		if _, statErr := os.Stat(filepath.Join(local, "afterburner.json")); statErr == nil {
			return local, registry.Source{Type: "path", Value: local}, func() {}, nil
		}
	}
	url := spec
	var ref *string
	shorthand := regexp.MustCompile(`^([\w.-]+/[\w.-]+)(?:@(.+))?$`).FindStringSubmatch(spec)
	if shorthand != nil {
		url = "https://github.com/" + shorthand[1] + ".git"
		if shorthand[2] != "" {
			value := shorthand[2]
			ref = &value
		}
	}
	if err := validateGitURL(url); err != nil {
		return "", registry.Source{}, nil, err
	}
	return m.cloneGit(url, ref)
}

func (m Manager) resolveRegistrySource(source registry.Source) (string, registry.Source, func(), error) {
	switch source.Type {
	case "path":
		path, err := filepath.Abs(source.Value)
		return path, registry.Source{Type: "path", Value: path}, func() {}, err
	case "git":
		return m.cloneGit(source.Value, source.Ref)
	case "archive":
		return m.extractExternalArchive(source.Value)
	default:
		return "", registry.Source{}, nil, fmt.Errorf("unsupported extension source type %q", source.Type)
	}
}

func (m Manager) extractExternalArchive(source string) (string, registry.Source, func(), error) {
	info, err := os.Stat(source)
	if err != nil {
		return "", registry.Source{}, nil, fmt.Errorf("inspect extension archive: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxPackageArchiveBytes {
		return "", registry.Source{}, nil, fmt.Errorf("extension archive has an invalid size")
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return "", registry.Source{}, nil, fmt.Errorf("read extension archive: %w", err)
	}
	if err := os.MkdirAll(m.Layout.Staging, 0o700); err != nil {
		return "", registry.Source{}, nil, err
	}
	target, err := os.MkdirTemp(m.Layout.Staging, "archive-extension-")
	if err != nil {
		return "", registry.Source{}, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(target) }
	if err := ExtractPackageArchive(data, target); err != nil {
		cleanup()
		return "", registry.Source{}, nil, err
	}
	sum := sha256.Sum256(data)
	absolute, err := filepath.Abs(source)
	if err != nil {
		cleanup()
		return "", registry.Source{}, nil, err
	}
	return target, registry.Source{
		Type:    "archive",
		Value:   absolute,
		Version: "sha256:" + hex.EncodeToString(sum[:]),
		Digest:  "sha256:" + hex.EncodeToString(sum[:]),
	}, cleanup, nil
}

func (m Manager) cloneGit(url string, ref *string) (string, registry.Source, func(), error) {
	if err := os.MkdirAll(m.Layout.Staging, 0o700); err != nil {
		return "", registry.Source{}, nil, err
	}
	target, err := os.MkdirTemp(m.Layout.Staging, "git-extension-")
	if err != nil {
		return "", registry.Source{}, nil, err
	}
	if err := os.Remove(target); err != nil {
		return "", registry.Source{}, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(target) }
	args := []string{"clone", "--quiet"}
	if ref != nil && *ref != "" {
		args = append(args, "--branch", *ref)
	}
	args = append(args, url, target)
	command := exec.Command("git", args...)
	if err := command.Run(); err != nil {
		cleanup()
		return "", registry.Source{}, nil, fmt.Errorf("clone extension Git source: %w", err)
	}
	commitCommand := exec.Command("git", "-C", target, "rev-parse", "HEAD")
	output, err := commitCommand.Output()
	if err != nil {
		cleanup()
		return "", registry.Source{}, nil, fmt.Errorf("resolve extension Git commit: %w", err)
	}
	metadata := registry.Source{
		Type: "git", Value: url, Ref: ref, Commit: strings.TrimSpace(string(output)),
	}
	return target, metadata, cleanup, nil
}

func validateGitURL(value string) error {
	if strings.Contains(value, "://") {
		parsed, err := url.Parse(value)
		if err != nil {
			return fmt.Errorf("invalid Git source URL")
		}
		if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return fmt.Errorf("Git source URLs may not contain credentials, query parameters, or fragments")
		}
	}
	return nil
}

func readManifest(source string) (registry.Manifest, error) {
	return readManifestWithCanonicalID(source, true)
}

// ValidatePackage validates an extension directory without installing or
// executing it.
func ValidatePackage(source string) (registry.Manifest, error) {
	absolute, err := filepath.Abs(source)
	if err != nil {
		return registry.Manifest{}, fmt.Errorf("resolve extension path: %w", err)
	}
	return readManifestWithCanonicalID(absolute, false)
}

// PackPackage writes a deterministic extension archive after validating the
// source package.
func PackPackage(source, destination string) error {
	source, err := filepath.Abs(source)
	if err != nil {
		return fmt.Errorf("resolve extension path: %w", err)
	}
	if _, err := ValidatePackage(source); err != nil {
		return err
	}
	destination, err = filepath.Abs(destination)
	if err != nil {
		return fmt.Errorf("resolve archive path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return fmt.Errorf("create archive directory: %w", err)
	}
	temp, err := os.CreateTemp(filepath.Dir(destination), ".afterburn-extension-*.zip")
	if err != nil {
		return fmt.Errorf("create extension archive: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	writer := zip.NewWriter(temp)
	var entryCount int
	var expandedBytes int64
	walkErr := filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == source {
			return nil
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if entry.IsDir() && relative == ".git" {
			return filepath.SkipDir
		}
		if entry.IsDir() {
			return nil
		}
		if samePath(path, destination) || samePath(path, tempPath) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("extension packages may not contain symbolic links: %s", relative)
		}
		entryCount++
		if entryCount > maxPackageArchiveEntries {
			return fmt.Errorf("extension package contains more than %d files", maxPackageArchiveEntries)
		}
		if info.Size() > maxPackageEntryBytes || info.Size() > maxPackageExpandedBytes ||
			expandedBytes > maxPackageExpandedBytes-info.Size() {
			return fmt.Errorf("extension package exceeds expanded size limits")
		}
		expandedBytes += info.Size()
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			file.Close()
			return err
		}
		header.Name = filepath.ToSlash(relative)
		header.Method = zip.Deflate
		header.Modified = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)
		target, err := writer.CreateHeader(header)
		if err != nil {
			file.Close()
			return err
		}
		_, copyErr := io.Copy(target, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	closeErr := writer.Close()
	fileErr := temp.Close()
	if walkErr != nil {
		return fmt.Errorf("pack extension: %w", walkErr)
	}
	if closeErr != nil {
		return fmt.Errorf("finalize extension archive: %w", closeErr)
	}
	if fileErr != nil {
		return fmt.Errorf("close extension archive: %w", fileErr)
	}
	info, err := os.Stat(tempPath)
	if err != nil {
		return fmt.Errorf("inspect extension archive: %w", err)
	}
	if info.Size() <= 0 || info.Size() > maxPackageArchiveBytes {
		return fmt.Errorf("extension archive size must be between 1 byte and %d bytes", maxPackageArchiveBytes)
	}
	if err := os.Rename(tempPath, destination); err != nil {
		return fmt.Errorf("publish extension archive: %w", err)
	}
	return nil
}

func samePath(left, right string) bool {
	return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
}

func readManifestWithCanonicalID(source string, canonicalize bool) (registry.Manifest, error) {
	data, err := os.ReadFile(filepath.Join(source, "afterburner.json"))
	if err != nil {
		return registry.Manifest{}, fmt.Errorf("read extension manifest: %w", err)
	}
	var manifest registry.Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return registry.Manifest{}, fmt.Errorf("parse extension manifest: %w", err)
	}
	if canonicalize {
		manifest.ID = canonicalID(manifest.ID)
	}
	registry.NormalizeManifest(&manifest)
	if manifest.SchemaVersion != 1 || !validID.MatchString(manifest.ID) || manifest.DisplayName == "" ||
		!registry.ValidVisibility(manifest.Visibility) || manifest.Requires.Afterburner == "" ||
		manifest.Runtime.Execution != "in-process" || manifest.Runtime.Entrypoint == "" {
		return registry.Manifest{}, fmt.Errorf("invalid afterburner.json manifest")
	}
	seenCapabilities := map[string]struct{}{}
	for _, capability := range manifest.Capabilities {
		if !validCapability.MatchString(capability) {
			return registry.Manifest{}, fmt.Errorf("invalid afterburner.json capability %q", capability)
		}
		if _, ok := seenCapabilities[capability]; ok {
			return registry.Manifest{}, fmt.Errorf("duplicate afterburner.json capability %q", capability)
		}
		seenCapabilities[capability] = struct{}{}
	}
	if err := registry.ValidateManifestUI(manifest); err != nil {
		return registry.Manifest{}, fmt.Errorf("invalid afterburner.json UI declaration: %w", err)
	}
	if err := validateCompatibilitySyntax(manifest); err != nil {
		return registry.Manifest{}, fmt.Errorf("invalid afterburner.json compatibility requirement: %w", err)
	}
	if manifest.SessionExtension != nil {
		if err := validateEntrypoint(source, manifest.SessionExtension.Entrypoint); err != nil {
			return registry.Manifest{}, err
		}
	}
	if err := validateEntrypoint(source, manifest.Runtime.Entrypoint); err != nil {
		return registry.Manifest{}, err
	}
	return manifest, nil
}

func validateEntrypoint(source, entrypoint string) error {
	target := filepath.Join(source, filepath.FromSlash(entrypoint))
	if !registry.Within(target, source) {
		return fmt.Errorf("entrypoint escapes extension package: %s", entrypoint)
	}
	info, err := os.Stat(target)
	if err != nil || info.IsDir() {
		return fmt.Errorf("entrypoint does not exist: %s", entrypoint)
	}
	return nil
}

func (m Manager) initializeBuiltinConfig(id, source string) error {
	var sourceConfig, target string
	switch id {
	case "byo-models":
		sourceConfig = filepath.Join(source, "extensions", "BYOModels", "models.example.json")
		target = m.Layout.BYOModelsConfig
	case "black-box":
		sourceConfig = filepath.Join(source, "config.example.json")
		target = filepath.Join(m.Layout.Config, "black-box.json")
	default:
		return nil
	}

	if _, err := os.Stat(target); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	data, err := os.ReadFile(sourceConfig)
	if err != nil {
		return fmt.Errorf("read default configuration for %s: %w", id, err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	return os.WriteFile(target, data, 0o600)
}

func hashFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func identityBinding(manifest registry.Manifest, source registry.Source, manifestHash, treeHash string, grantEpoch uint64, boundAt string, trustedBuiltin bool) registry.IdentityBinding {
	if grantEpoch == 0 {
		grantEpoch = 1
	}
	builtinSigned := trustedBuiltin && manifest.Visibility == "builtin" && registry.IsTrustedBuiltinSourceType(source.Type) && source.Value == manifest.ID
	binding := registry.IdentityBinding{
		ExtensionID:   manifest.ID,
		ManifestHash:  "sha256:" + manifestHash,
		TreeHash:      "sha256:" + treeHash,
		SourceType:    source.Type,
		SourceValue:   source.Value,
		SourceVersion: source.Version,
		SourceCommit:  source.Commit,
		RegistryEpoch: uint64(time.Now().UTC().UnixNano()),
		GrantEpoch:    grantEpoch,
		BoundAt:       boundAt,
		BuiltinSigned: builtinSigned,
	}
	if binding.BuiltinSigned {
		binding.SignerID = "afterburner-release"
		binding.SignerFingerprint = source.SignerFingerprint
	} else if source.Commit != "" {
		binding.SignerID = "git"
		binding.SignerFingerprint = source.Commit
	} else {
		binding.SignerID = "local"
		binding.SignerFingerprint = binding.TreeHash
	}
	return binding
}

func hashTree(root string) (string, error) {
	hash := sha256.New()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("extension packages may not contain symbolic links: %s", relative)
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		hash.Write([]byte(filepath.ToSlash(relative)))
		hash.Write([]byte{0})
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		hash.Write(data)
		hash.Write([]byte{0})
		return nil
	})
	return hex.EncodeToString(hash.Sum(nil)), err
}

func copyTree(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return os.MkdirAll(destination, 0o700)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("extension packages may not contain symbolic links: %s", relative)
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(destination, relative), 0o700)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(destination, relative), data, 0o600)
	})
}

func acquireLock(path string) (func(), error) {
	deadline := time.Now().Add(30 * time.Second)
	for {
		err := os.Mkdir(path, 0o700)
		if err == nil {
			return func() { _ = os.Remove(path) }, nil
		}
		if !os.IsExist(err) {
			return nil, fmt.Errorf("acquire registry lock: %w", err)
		}
		info, statErr := os.Stat(path)
		if statErr == nil && time.Since(info.ModTime()) > 2*time.Minute {
			_ = os.Remove(path)
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting for extension registry lock")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func normalizeIDs(ids []string) ([]string, error) {
	seen := map[string]bool{}
	result := make([]string, len(ids))
	for i, id := range ids {
		id = canonicalID(id)
		if !validID.MatchString(id) {
			return nil, fmt.Errorf("invalid extension ID %q", id)
		}
		if seen[id] {
			return nil, fmt.Errorf("duplicate extension ID %q", id)
		}
		seen[id] = true
		result[i] = id
	}
	return result, nil
}

func canonicalID(id string) string {
	switch id {
	case "byomodels":
		return "byo-models"
	case registry.LegacyOpenAIServerID:
		return registry.OpenAIServerID
	default:
		return id
	}
}

func builtinDefaultEnabled(id string) bool {
	return id != registry.OpenAIServerID
}

func registryEntryKey(value *registry.Registry, id string) string {
	if value == nil {
		return id
	}
	if _, ok := value.Extensions[id]; ok {
		return id
	}
	if id == registry.OpenAIServerID {
		if _, ok := value.Extensions[registry.LegacyOpenAIServerID]; ok {
			return registry.LegacyOpenAIServerID
		}
	}
	return id
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
