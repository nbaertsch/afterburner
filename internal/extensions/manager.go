package extensions

import (
	"archive/zip"
	"bytes"
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

type Manager struct {
	Layout home.Layout
	Stdout io.Writer
	// BuiltinFetcher, when set, is consulted before the embedded release
	// asset for each built-in extension ID during InstallBuiltins. It lets
	// callers (e.g. the CLI's install command) source built-ins from the
	// current signed GitHub release instead of the binary's go:embed'd
	// snapshot, so a built-in extension update no longer requires a full
	// core rebuild. It is nil by default, preserving prior behavior.
	BuiltinFetcher BuiltinFetcher
}

// BuiltinFetcher resolves a built-in extension ID to an extracted, verified
// source directory. Implementations are responsible for any network fetch
// and signature verification; a successful result is treated as a signed
// release built-in and bound to its source and package hashes. FetchBuiltin
// should return an error for any ID it cannot resolve so InstallBuiltins can
// fall back to the embedded release asset.
type BuiltinFetcher interface {
	FetchBuiltin(id string) (path string, cleanup func(), err error)
}

// builtinSourceOverrides parses AFTERBURNER_BUILTIN_SOURCE_OVERRIDE. Local overrides are
// intentionally not trusted as signed built-ins; syncBuiltins will reject them before
// persisting a built-in identity. Format: "id=path[,id2=path2,...]".
func builtinSourceOverrides() map[string]string {
	raw := strings.TrimSpace(os.Getenv("AFTERBURNER_BUILTIN_SOURCE_OVERRIDE"))
	if raw == "" {
		return nil
	}
	overrides := map[string]string{}
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		id, path, found := strings.Cut(entry, "=")
		id = canonicalID(strings.TrimSpace(id))
		path = strings.TrimSpace(path)
		if !found || id == "" || path == "" {
			continue
		}
		overrides[id] = path
	}
	return overrides
}

func (m Manager) InstallBuiltins(ids []string) error {
	return m.syncBuiltins(ids, false)
}

func (m Manager) SyncBuiltins(ids []string) error {
	return m.syncBuiltins(ids, true)
}

func (m Manager) syncBuiltins(ids []string, requireFetcher bool) error {
	return m.withRegistry(func(value *registry.Registry) error {
		catalog := assets.Builtins()
		overrides := builtinSourceOverrides()
		if len(ids) == 0 {
			for id := range catalog {
				ids = append(ids, id)
			}
			for id := range overrides {
				if _, ok := catalog[id]; !ok {
					ids = append(ids, id)
				}
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
			overridePath, hasOverride := overrides[id]
			if !knownBuiltin && !hasOverride {
				return fmt.Errorf("unknown built-in extension %q; available: %s", id, strings.Join(sortedKeys(catalog), ", "))
			}
			var source string
			var sourceMetadata registry.Source
			var cleanup func()
			if hasOverride {
				resolved, err := filepath.Abs(overridePath)
				if err != nil {
					return fmt.Errorf("resolve built-in source override for %q: %w", id, err)
				}
				manifest, err := readManifest(resolved)
				if err != nil || manifest.ID != id || manifest.Visibility != "builtin" {
					return fmt.Errorf("built-in source override for %q at %s has an invalid or mismatched manifest", id, resolved)
				}
				source, sourceMetadata, cleanup = resolved, registry.Source{Type: "path", Value: resolved}, func() {}
				fmt.Fprintf(m.Stdout, "Using development source override for built-in extension %q at %s.\n", id, resolved)
			} else if m.BuiltinFetcher != nil {
				fetched, fetchCleanup, err := m.BuiltinFetcher.FetchBuiltin(id)
				if err != nil {
					if requireFetcher || !knownBuiltin {
						return fmt.Errorf("fetch built-in extension %q: %w", id, err)
					}
					fmt.Fprintf(m.Stdout, "Could not fetch built-in extension %q from the current release (%v); using the built-in version bundled with this binary.\n", id, err)
					archive := catalog[id]
					extracted, extractCleanup, err := m.extractBuiltin(id, archive)
					if err != nil {
						return err
					}
					source, sourceMetadata, cleanup = extracted, registry.Source{Type: "embedded", Value: id}, extractCleanup
				} else {
					manifest, err := readManifest(fetched)
					if err != nil || manifest.ID != id || manifest.Visibility != "builtin" {
						fetchCleanup()
						return fmt.Errorf("fetched built-in extension %q has an invalid or mismatched manifest", id)
					}
					source, sourceMetadata, cleanup = fetched, registry.Source{Type: "signed-release", Value: id}, fetchCleanup
				}
			} else {
				if requireFetcher {
					return fmt.Errorf("built-in extension %q must be synced from a pinned release", id)
				}
				archive := catalog[id]
				extracted, extractCleanup, err := m.extractBuiltin(id, archive)
				if err != nil {
					return err
				}
				source, sourceMetadata, cleanup = extracted, registry.Source{Type: "embedded", Value: id}, extractCleanup
			}
			previous := value.Extensions[id]
			enabled := builtinDefaultEnabled(id)
			if previous.Manifest.ID == id {
				enabled = previous.Enabled
			}
			trustedBuiltin := !hasOverride
			entry, err := m.materialize(source, sourceMetadata, previous, enabled, trustedBuiltin, false)
			if err != nil {
				cleanup()
				return err
			}
			value.Extensions[id] = entry
			if requireFetcher && previous.ActivePath == entry.ActivePath {
				fmt.Fprintf(m.Stdout, "Built-in extension %q is already synced at %s.\n", id, filepath.Base(entry.ActivePath))
			} else if requireFetcher {
				fmt.Fprintf(m.Stdout, "Synced built-in extension %q to %s.\n", id, filepath.Base(entry.ActivePath))
			} else if entry.Enabled {
				fmt.Fprintf(m.Stdout, "Installed and enabled built-in extension %q at %s.\n", id, filepath.Base(entry.ActivePath))
			} else {
				fmt.Fprintf(m.Stdout, "Installed built-in extension %q disabled at %s.\n", id, filepath.Base(entry.ActivePath))
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
		if current.Source.Type != "path" && current.Source.Type != "git" {
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
		case "path", "git":
			if err := m.Update(id); err != nil {
				return err
			}
		case "embedded", "signed-release":
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
		previous := *entry.PreviousActivePath
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
		previousSource := entry.PreviousSource
		entry.ActivePath = previous
		entry.PreviousActivePath = &current
		entry.PreviousSource = &currentSource
		entry.Manifest = manifest
		if previousSource != nil {
			entry.Source = *previousSource
		} else if legacyOpenAIServerRollback {
			entry.Source = registry.Source{Type: "path", Value: previous}
		} else {
			entry.Source.Version = filepath.Base(previous)
		}
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
	if err := update(&value); err != nil {
		return err
	}
	if value.Epoch == 0 {
		value.Epoch = 1
	}
	value.Epoch++
	if err := registry.Save(m.Layout.Root, value); err != nil {
		return err
	}
	return sessions.Reconcile(m.Layout, value)
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
	treeHash, err := registry.HashTree(source)
	if err != nil {
		return registry.Entry{}, err
	}
	manifestHash, err := registry.HashFile(filepath.Join(source, "afterburner.json"))
	if err != nil {
		return registry.Entry{}, err
	}
	version := "local-" + treeHash[:16]
	target := filepath.Join(m.Layout.Extensions, manifest.ID, version)
	if !registry.Within(target, m.Layout.Extensions) {
		return registry.Entry{}, fmt.Errorf("extension target escapes managed root")
	}
	if _, err := os.Stat(target); os.IsNotExist(err) {
		staging := target + fmt.Sprintf(".staging-%d", os.Getpid())
		if err := os.RemoveAll(staging); err != nil {
			return registry.Entry{}, err
		}
		if err := copyTree(source, staging); err != nil {
			os.RemoveAll(staging)
			return registry.Entry{}, err
		}
		if manifest.SessionExtension != nil {
			if err := installCopilotSDKShim(m.Layout, staging); err != nil {
				os.RemoveAll(staging)
				return registry.Entry{}, err
			}
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			os.RemoveAll(staging)
			return registry.Entry{}, err
		}
		if err := os.Rename(staging, target); err != nil {
			os.RemoveAll(staging)
			if _, statErr := os.Stat(target); statErr != nil {
				return registry.Entry{}, err
			}
		}
	} else if err != nil {
		return registry.Entry{}, err
	} else if manifest.SessionExtension != nil {
		if err := installCopilotSDKShim(m.Layout, target); err != nil {
			return registry.Entry{}, err
		}
	}
	var previousPath *string
	var previousSource *registry.Source
	if previous.ActivePath != "" && previous.ActivePath != target {
		value := previous.ActivePath
		previousPath = &value
		source := previous.Source
		previousSource = &source
	} else {
		previousPath = previous.PreviousActivePath
		previousSource = previous.PreviousSource
	}
	sourceMetadata.Version = version
	now := time.Now().UTC().Format(time.RFC3339Nano)
	identity := identityBinding(manifest, sourceMetadata, manifestHash, treeHash, previous.Identity.GrantEpoch, now, trustedBuiltin)
	entry := registry.Entry{
		Enabled:            enabled,
		ActivePath:         target,
		PreviousActivePath: previousPath,
		PreviousSource:     previousSource,
		Manifest:           manifest,
		Source:             sourceMetadata,
		Identity:           identity,
		UpdatedAt:          now,
	}
	return registry.SealEntry(m.Layout.Root, entry)
}

func (m Manager) resolveSource(spec string) (string, registry.Source, func(), error) {
	if spec == "" {
		return "", registry.Source{}, nil, fmt.Errorf("an extension source is required")
	}
	if local, err := filepath.Abs(spec); err == nil {
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
	default:
		return "", registry.Source{}, nil, fmt.Errorf("unsupported extension source type %q", source.Type)
	}
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

func (m Manager) extractBuiltin(id string, archive []byte) (string, func(), error) {
	if err := os.MkdirAll(m.Layout.Staging, 0o700); err != nil {
		return "", nil, err
	}
	target, err := os.MkdirTemp(m.Layout.Staging, "builtin-"+id+"-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(target) }
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("open embedded built-in %q: %w", id, err)
	}
	for _, file := range reader.File {
		path := filepath.Join(target, filepath.FromSlash(file.Name))
		if !registry.Within(path, target) || file.FileInfo().IsDir() {
			cleanup()
			return "", nil, fmt.Errorf("invalid embedded built-in path %q", file.Name)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			cleanup()
			return "", nil, err
		}
		input, err := file.Open()
		if err != nil {
			cleanup()
			return "", nil, err
		}
		data, readErr := io.ReadAll(io.LimitReader(input, 64<<20))
		closeErr := input.Close()
		if readErr != nil || closeErr != nil || uint64(len(data)) != file.UncompressedSize64 {
			cleanup()
			return "", nil, fmt.Errorf("read embedded built-in file %q", file.Name)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			cleanup()
			return "", nil, err
		}
	}
	manifest, err := readManifest(target)
	if err != nil || manifest.ID != id || manifest.Visibility != "builtin" {
		cleanup()
		return "", nil, fmt.Errorf("embedded built-in %q manifest is invalid", id)
	}
	return target, cleanup, nil
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
		binding.SignerID = "afterburner-core"
		binding.SignerFingerprint = "builtin:" + manifest.ID
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
			if entry.Name() == ".git" || entry.Name() == "node_modules" || entry.Name() == "bin" || entry.Name() == "obj" {
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

func installCopilotSDKShim(layout home.Layout, activePath string) error {
	source, err := findCopilotSDK(layout)
	if err != nil {
		return nil
	}
	target := filepath.Join(activePath, "node_modules", "@github", "copilot-sdk")
	if err := os.RemoveAll(target); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	if err := copyTree(source, target); err != nil {
		return err
	}
	packageJSON := []byte(`{"name":"@github/copilot-sdk","type":"module","exports":{".":"./index.js","./extension":"./extension.js"}}` + "\n")
	return os.WriteFile(filepath.Join(target, "package.json"), packageJSON, 0o600)
}

func findCopilotSDK(layout home.Layout) (string, error) {
	root := filepath.Join(layout.NormalCopilotHome, "pkg", "win32-x64")
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", fmt.Errorf("locate Copilot SDK package root: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() > entries[j].Name() })
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		candidate := filepath.Join(root, entry.Name(), "copilot-sdk")
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("locate Copilot SDK package in %s", root)
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
			if entry.Name() == ".git" || entry.Name() == "node_modules" || entry.Name() == "bin" || entry.Name() == "obj" {
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
