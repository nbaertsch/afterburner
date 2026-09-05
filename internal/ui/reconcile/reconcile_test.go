package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/nbaertsch/afterburner/internal/ui/bridge"
	"github.com/nbaertsch/afterburner/internal/ui/component"
	"github.com/nbaertsch/afterburner/internal/ui/protocol"
)

func TestApplyPatchIsAtomicOnInvalidOperation(t *testing.T) {
	store := NewStore("s1")
	initial := fixtureTree(1)
	snap, err := store.ApplySnapshot(initial)
	if err != nil {
		t.Fatal(err)
	}
	patch := patchFor("s1", 1, 2,
		bridge.PatchOp{Op: bridge.PatchSetProps, Path: "/id/name", Value: raw(`{"text":"updated"}`)},
		bridge.PatchOp{Op: bridge.PatchRemove, Path: "/id/missing"},
	)
	if _, err := store.ApplyPatch(context.Background(), patch, ApplyOptions{BaseDigest: snap.Digest}); err == nil {
		t.Fatal("expected invalid patch error")
	}
	after := store.Snapshot()
	if after.Revision != 1 || after.Digest != snap.Digest || string(after.Tree.Root.Children[0].Props) != `{"text":"hello"}` {
		t.Fatalf("patch was not atomic: %#v", after)
	}
}

func TestPatchConflictRequestsSnapshot(t *testing.T) {
	store := NewStore("s1")
	snap, err := store.ApplySnapshot(fixtureTree(3))
	if err != nil {
		t.Fatal(err)
	}
	patch := patchFor("s1", 2, 4, bridge.PatchOp{Op: bridge.PatchSetProps, Path: "/id/name", Value: raw(`{"text":"updated"}`)})
	result, err := store.ApplyPatch(context.Background(), patch, ApplyOptions{BaseDigest: snap.Digest})
	if !errors.Is(err, ErrConflict) || !result.Conflict || !result.RequestSnapshot || result.Snapshot.Revision != 3 {
		t.Fatalf("conflict result=(%#v), err=%v", result, err)
	}
}

func TestStatePreservedByStableIDAndKey(t *testing.T) {
	store := NewStore("s1")
	if _, err := store.ApplySnapshot(fixtureTree(1)); err != nil {
		t.Fatal(err)
	}
	store.SetState(StableState{FocusedID: "name", Scroll: map[string]ScrollPosition{"key:list": {Y: 42}, "gone": {Y: 1}}, Forms: map[string]FormState{"name": {Dirty: true}}})
	next := fixtureTree(2)
	next.Root.Children = []component.Node{
		{ID: "other", Kind: component.KindText, Props: raw(`{"text":"new"}`)},
		{ID: "name", Key: "name-input", Kind: component.KindTextInput, Props: raw(`{"value":"hello"}`)},
		{ID: "list2", Key: "list", Kind: component.KindList},
	}
	snap, err := store.ApplySnapshot(next)
	if err != nil {
		t.Fatal(err)
	}
	if snap.State.FocusedID != "name" || snap.State.Scroll["key:list"].Y != 42 || !snap.State.Forms["name"].Dirty {
		t.Fatalf("stable state not preserved: %#v", snap.State)
	}
	if _, ok := snap.State.Scroll["gone"]; ok {
		t.Fatalf("stale scroll entry preserved: %#v", snap.State.Scroll)
	}
}

func TestPatchFixtureApplies(t *testing.T) {
	store := NewStore("s1")
	if _, err := store.ApplySnapshot(fixtureTree(1)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("testdata/fixtures/patch-set-props.valid.json")
	if err != nil {
		t.Fatal(err)
	}
	var patch bridge.Patch
	if err := json.Unmarshal(data, &patch); err != nil {
		t.Fatal(err)
	}
	result, err := store.ApplyPatch(context.Background(), patch, ApplyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Snapshot.Tree.Root.Children[0].Props) != `{"value":"fixture"}` {
		t.Fatalf("fixture patch did not apply: %s", result.Snapshot.Tree.Root.Children[0].Props)
	}
}

func TestIDAndKeyAddressedPatch(t *testing.T) {
	store := NewStore("s1")
	if _, err := store.ApplySnapshot(fixtureTree(1)); err != nil {
		t.Fatal(err)
	}
	child := component.Node{ID: "added", Key: "added-key", Kind: component.KindText, Props: raw(`{"text":"added"}`)}
	childRaw, _ := json.Marshal(child)
	patch := patchFor("s1", 1, 2,
		bridge.PatchOp{Op: bridge.PatchSetProps, Path: "/key/name-input", Value: raw(`{"value":"patched"}`)},
		bridge.PatchOp{Op: bridge.PatchAdd, Path: "/id/list/children/-", Value: childRaw},
	)
	result, err := store.ApplyPatch(context.Background(), patch, ApplyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Snapshot.Tree.Root.Children[0].ID != "name" || string(result.Snapshot.Tree.Root.Children[0].Props) != `{"value":"patched"}` {
		t.Fatalf("id/key setProps failed: %#v", result.Snapshot.Tree.Root.Children[0])
	}
	if got := result.Snapshot.Tree.Root.Children[1].Children[0].ID; got != "added" {
		t.Fatalf("child not added by id address: %s", got)
	}
}

func FuzzPatchSetPropsPreservesValidTree(f *testing.F) {
	f.Add("seed")
	f.Add("value with unicode π")
	f.Fuzz(func(t *testing.T, text string) {
		store := NewStore("s1")
		if _, err := store.ApplySnapshot(fixtureTree(1)); err != nil {
			t.Fatal(err)
		}
		payload, _ := json.Marshal(map[string]string{"value": text})
		patch := patchFor("s1", 1, 2, bridge.PatchOp{Op: bridge.PatchSetProps, Path: "/id/name", Value: payload})
		result, err := store.ApplyPatch(context.Background(), patch, ApplyOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if err := ValidateTree(result.Snapshot.Tree); err != nil {
			t.Fatal(err)
		}
	})
}

func fixtureTree(rev uint64) component.Tree {
	return component.Tree{SurfaceID: "s1", Revision: rev, Root: component.Node{ID: "root", Kind: component.KindSurface, Children: []component.Node{
		{ID: "name", Key: "name-input", Kind: component.KindTextInput, Props: raw(`{"text":"hello"}`)},
		{ID: "list", Key: "list", Kind: component.KindList},
	}}}
}

func patchFor(surfaceID string, base, next uint64, ops ...bridge.PatchOp) bridge.Patch {
	return bridge.Patch{SchemaVersion: protocol.SchemaVersion, Protocol: protocol.Protocol, Revision: protocol.ProtocolRevision, SurfaceID: surfaceID, BaseRevision: base, NextRevision: next, Operations: ops}
}

func raw(s string) json.RawMessage { return json.RawMessage(s) }
