package reconcile

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"

	"github.com/nbaertsch/afterburner/internal/ui/bridge"
	"github.com/nbaertsch/afterburner/internal/ui/component"
	uierrors "github.com/nbaertsch/afterburner/internal/ui/errors"
	"github.com/nbaertsch/afterburner/internal/ui/protocol"
)

var (
	ErrConflict     = errors.New("reconcile conflict")
	ErrInvalidPatch = errors.New("invalid patch")
)

type ScrollPosition struct {
	X int `json:"x"`
	Y int `json:"y"`
}

type FormState struct {
	Values map[string]json.RawMessage `json:"values,omitempty"`
	Dirty  bool                       `json:"dirty,omitempty"`
}

type TableState struct {
	SortBy       string   `json:"sortBy,omitempty"`
	Descending   bool     `json:"descending,omitempty"`
	SelectedRows []string `json:"selectedRows,omitempty"`
}

type TreeState struct {
	Expanded []string `json:"expanded,omitempty"`
	Selected []string `json:"selected,omitempty"`
}

type StableState struct {
	FocusedID string                     `json:"focusedId,omitempty"`
	Selection map[string][]string        `json:"selection,omitempty"`
	Scroll    map[string]ScrollPosition  `json:"scroll,omitempty"`
	Forms     map[string]FormState       `json:"forms,omitempty"`
	Tables    map[string]TableState      `json:"tables,omitempty"`
	Trees     map[string]TreeState       `json:"trees,omitempty"`
	Custom    map[string]json.RawMessage `json:"custom,omitempty"`
}

type Snapshot struct {
	Tree     component.Tree `json:"tree"`
	Revision uint64         `json:"revision"`
	Digest   string         `json:"digest"`
	State    StableState    `json:"state,omitempty"`
}

type ApplyOptions struct {
	BaseDigest string
}

type ApplyResult struct {
	Snapshot        Snapshot
	Conflict        bool
	RequestSnapshot bool
	ConflictReason  string
}

type ConflictError struct {
	SurfaceID        string
	ExpectedRevision uint64
	ActualRevision   uint64
	ExpectedDigest   string
	ActualDigest     string
	Reason           string
}

func (e ConflictError) Error() string {
	return fmt.Sprintf("%s: %s for surface %s", ErrConflict, e.Reason, e.SurfaceID)
}

func (e ConflictError) Is(target error) bool { return target == ErrConflict }

type Store struct {
	mu      sync.RWMutex
	surface string
	tree    component.Tree
	digest  string
	state   StableState
}

func NewStore(surfaceID string) *Store {
	return &Store{surface: surfaceID, state: StableState{}}
}

func (s *Store) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return Snapshot{Tree: cloneTree(s.tree), Revision: s.tree.Revision, Digest: s.digest, State: cloneState(s.state)}
}

func (s *Store) SetState(state StableState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = normalizeState(state).PreserveFor(s.tree)
}

func (s *Store) ApplySnapshot(tree component.Tree) (Snapshot, error) {
	if err := ValidateTree(tree); err != nil {
		return Snapshot{}, err
	}
	if s.surface != "" && tree.SurfaceID != s.surface {
		return Snapshot{}, uierrors.ContractError{Code: uierrors.InvalidComponentTree, Message: "snapshot surface mismatch", Target: tree.SurfaceID, Recoverable: false}
	}
	digest, err := DigestTree(tree)
	if err != nil {
		return Snapshot{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = s.state.PreserveFor(tree)
	s.tree = cloneTree(tree)
	s.digest = digest
	return Snapshot{Tree: cloneTree(s.tree), Revision: s.tree.Revision, Digest: s.digest, State: cloneState(s.state)}, nil
}

func (s *Store) ApplyPatch(ctx context.Context, patch bridge.Patch, opts ApplyOptions) (ApplyResult, error) {
	if err := ctx.Err(); err != nil {
		return ApplyResult{}, err
	}
	if patch.SchemaVersion != protocol.SchemaVersion || patch.Protocol != protocol.Protocol || patch.Revision != protocol.ProtocolRevision {
		return ApplyResult{}, uierrors.ContractError{Code: uierrors.InvalidPatch, Message: "unsupported patch revision", Recoverable: false}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.surface != "" && patch.SurfaceID != s.surface {
		return ApplyResult{}, uierrors.ContractError{Code: uierrors.InvalidPatch, Message: "patch surface mismatch", Target: patch.SurfaceID, Recoverable: false}
	}
	if patch.BaseRevision != s.tree.Revision {
		return conflictResultLocked(s, patch, opts.BaseDigest, "base revision mismatch")
	}
	if opts.BaseDigest != "" && opts.BaseDigest != s.digest {
		return conflictResultLocked(s, patch, opts.BaseDigest, "base digest mismatch")
	}
	if patch.NextRevision < patch.BaseRevision || (patch.NextRevision == patch.BaseRevision && len(patch.Operations) > 0) {
		return ApplyResult{}, uierrors.ContractError{Code: uierrors.InvalidPatch, Message: "next revision must advance when operations are present", Target: "nextRevision", Recoverable: false}
	}
	if patch.NextRevision == patch.BaseRevision && len(patch.Operations) == 0 {
		snap := Snapshot{Tree: cloneTree(s.tree), Revision: s.tree.Revision, Digest: s.digest, State: cloneState(s.state)}
		return ApplyResult{Snapshot: snap}, nil
	}
	next := cloneTree(s.tree)
	for i, op := range patch.Operations {
		if err := ctx.Err(); err != nil {
			return ApplyResult{}, err
		}
		if err := applyOp(&next, op); err != nil {
			return ApplyResult{}, fmt.Errorf("operation %d: %w", i, err)
		}
	}
	next.Revision = patch.NextRevision
	if err := ValidateTree(next); err != nil {
		return ApplyResult{}, err
	}
	digest, err := DigestTree(next)
	if err != nil {
		return ApplyResult{}, err
	}
	s.state = s.state.PreserveFor(next)
	s.tree = next
	s.digest = digest
	snap := Snapshot{Tree: cloneTree(s.tree), Revision: s.tree.Revision, Digest: s.digest, State: cloneState(s.state)}
	return ApplyResult{Snapshot: snap}, nil
}

func conflictResultLocked(s *Store, patch bridge.Patch, expectedDigest, reason string) (ApplyResult, error) {
	snap := Snapshot{Tree: cloneTree(s.tree), Revision: s.tree.Revision, Digest: s.digest, State: cloneState(s.state)}
	err := ConflictError{SurfaceID: patch.SurfaceID, ExpectedRevision: patch.BaseRevision, ActualRevision: s.tree.Revision, ExpectedDigest: expectedDigest, ActualDigest: s.digest, Reason: reason}
	return ApplyResult{Snapshot: snap, Conflict: true, RequestSnapshot: true, ConflictReason: reason}, err
}

type Engine struct{}

func (Engine) Validate(_ context.Context, tree component.Tree) error { return ValidateTree(tree) }

func (Engine) Diff(_ context.Context, previous, next component.Tree) (bridge.Patch, error) {
	if err := ValidateTree(next); err != nil {
		return bridge.Patch{}, err
	}
	prevDigest, _ := DigestTree(previous)
	nextDigest, _ := DigestTree(next)
	if prevDigest == nextDigest {
		return bridge.Patch{SchemaVersion: protocol.SchemaVersion, Protocol: protocol.Protocol, Revision: protocol.ProtocolRevision, SurfaceID: next.SurfaceID, BaseRevision: previous.Revision, NextRevision: previous.Revision}, nil
	}
	payload, err := json.Marshal(next.Root)
	if err != nil {
		return bridge.Patch{}, err
	}
	return bridge.Patch{SchemaVersion: protocol.SchemaVersion, Protocol: protocol.Protocol, Revision: protocol.ProtocolRevision, SurfaceID: next.SurfaceID, BaseRevision: previous.Revision, NextRevision: next.Revision, Operations: []bridge.PatchOp{{Op: bridge.PatchReplace, Path: "/root", Value: payload}}}, nil
}

func (Engine) Apply(ctx context.Context, base component.Tree, patch bridge.Patch) (component.Tree, error) {
	store := NewStore(base.SurfaceID)
	if _, err := store.ApplySnapshot(base); err != nil {
		return component.Tree{}, err
	}
	result, err := store.ApplyPatch(ctx, patch, ApplyOptions{})
	if err != nil {
		return component.Tree{}, err
	}
	return result.Snapshot.Tree, nil
}

func DigestTree(tree component.Tree) (string, error) {
	buf := bytes.Buffer{}
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(tree); err != nil {
		return "", err
	}
	sum := sha256.Sum256(bytes.TrimSpace(buf.Bytes()))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func ValidateTree(tree component.Tree) error {
	if tree.SurfaceID == "" {
		return uierrors.ContractError{Code: uierrors.InvalidComponentTree, Message: "surfaceId is required", Target: "surfaceId", Recoverable: false}
	}
	if tree.Root.ID == "" || tree.Root.Kind == "" {
		return uierrors.ContractError{Code: uierrors.InvalidComponentTree, Message: "root id and kind are required", Target: "root", Recoverable: false}
	}
	ids := map[string]bool{}
	var walk func(component.Node) error
	walk = func(n component.Node) error {
		if n.ID == "" || n.Kind == "" {
			return uierrors.ContractError{Code: uierrors.InvalidComponentTree, Message: "node id and kind are required", Recoverable: false}
		}
		if ids[n.ID] {
			return uierrors.ContractError{Code: uierrors.InvalidComponentTree, Message: "duplicate node id", Target: n.ID, Recoverable: false}
		}
		ids[n.ID] = true
		for _, child := range n.Children {
			if err := walk(child); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(tree.Root)
}

func (s StableState) PreserveFor(tree component.Tree) StableState {
	ids := collectStableAddresses(tree)
	out := normalizeState(s)
	if out.FocusedID != "" && !ids[out.FocusedID] {
		out.FocusedID = ""
	}
	filterStringSliceMaps(ids, out.Selection)
	filterMap(ids, out.Scroll)
	filterMap(ids, out.Forms)
	filterMap(ids, out.Tables)
	filterMap(ids, out.Trees)
	return out
}

func collectStableAddresses(tree component.Tree) map[string]bool {
	ids := map[string]bool{tree.SurfaceID: true}
	var walk func(component.Node)
	walk = func(n component.Node) {
		if n.ID != "" {
			ids[n.ID] = true
		}
		if n.Key != "" {
			ids[n.Key] = true
			ids["key:"+n.Key] = true
		}
		for _, child := range n.Children {
			walk(child)
		}
	}
	walk(tree.Root)
	return ids
}

func filterStringSliceMaps(valid map[string]bool, values map[string][]string) {
	for key := range values {
		if !valid[key] {
			delete(values, key)
		}
	}
}

func filterMap[T any](valid map[string]bool, values map[string]T) {
	for key := range values {
		if !valid[key] {
			delete(values, key)
		}
	}
}

func normalizeState(s StableState) StableState {
	if s.Selection == nil {
		s.Selection = map[string][]string{}
	}
	if s.Scroll == nil {
		s.Scroll = map[string]ScrollPosition{}
	}
	if s.Forms == nil {
		s.Forms = map[string]FormState{}
	}
	if s.Tables == nil {
		s.Tables = map[string]TableState{}
	}
	if s.Trees == nil {
		s.Trees = map[string]TreeState{}
	}
	if s.Custom == nil {
		s.Custom = map[string]json.RawMessage{}
	}
	return s
}

func cloneTree(tree component.Tree) component.Tree {
	data, _ := json.Marshal(tree)
	var out component.Tree
	_ = json.Unmarshal(data, &out)
	return out
}

func cloneState(state StableState) StableState {
	data, _ := json.Marshal(state)
	var out StableState
	_ = json.Unmarshal(data, &out)
	return normalizeState(out)
}

func applyOp(tree *component.Tree, op bridge.PatchOp) error {
	switch op.Op {
	case bridge.PatchTest:
		actual, err := readRaw(tree, op.Path)
		if err != nil {
			return err
		}
		if !jsonEqual(actual, op.Value) {
			return fmt.Errorf("%w: test failed at %s", ErrInvalidPatch, op.Path)
		}
		return nil
	case bridge.PatchAdd:
		return addValue(tree, op.Path, op.Value)
	case bridge.PatchRemove:
		return removeValue(tree, op.Path)
	case bridge.PatchReplace:
		return replaceValue(tree, op.Path, op.Value)
	case bridge.PatchSetProps:
		node, suffix, err := locateNode(tree, op.Path)
		if err != nil {
			return err
		}
		if len(suffix) != 0 {
			return fmt.Errorf("%w: setProps requires node address", ErrInvalidPatch)
		}
		node.Props = cloneRaw(op.Value)
		return nil
	case bridge.PatchBindData:
		node, suffix, err := locateNode(tree, op.Path)
		if err != nil {
			return err
		}
		if len(suffix) != 0 {
			return fmt.Errorf("%w: bindData requires node address", ErrInvalidPatch)
		}
		return bindData(node, op.Value)
	case bridge.PatchBindAction:
		node, suffix, err := locateNode(tree, op.Path)
		if err != nil {
			return err
		}
		if len(suffix) != 0 {
			return fmt.Errorf("%w: bindAction requires node address", ErrInvalidPatch)
		}
		return bindAction(node, op.Value)
	case bridge.PatchCopy, bridge.PatchMove:
		raw, err := readRaw(tree, op.From)
		if err != nil {
			return err
		}
		if op.Op == bridge.PatchMove {
			if err := removeValue(tree, op.From); err != nil {
				return err
			}
		}
		return addValue(tree, op.Path, raw)
	default:
		return fmt.Errorf("%w: unsupported operation %q", ErrInvalidPatch, op.Op)
	}
}

func addValue(tree *component.Tree, path string, value json.RawMessage) error {
	node, suffix, err := locateNode(tree, path)
	if err != nil {
		return err
	}
	if len(suffix) >= 1 && suffix[0] == "children" {
		var child component.Node
		if err := json.Unmarshal(value, &child); err != nil {
			return fmt.Errorf("%w: child payload: %v", ErrInvalidPatch, err)
		}
		index := len(node.Children)
		if len(suffix) > 1 && suffix[1] != "-" {
			parsed, err := strconv.Atoi(suffix[1])
			if err != nil || parsed < 0 || parsed > len(node.Children) {
				return fmt.Errorf("%w: invalid child index", ErrInvalidPatch)
			}
			index = parsed
		}
		node.Children = append(node.Children, component.Node{})
		copy(node.Children[index+1:], node.Children[index:])
		node.Children[index] = child
		return nil
	}
	return fmt.Errorf("%w: add supports children paths", ErrInvalidPatch)
}

func replaceValue(tree *component.Tree, path string, value json.RawMessage) error {
	node, suffix, err := locateNode(tree, path)
	if err != nil {
		return err
	}
	if len(suffix) == 0 {
		var next component.Node
		if err := json.Unmarshal(value, &next); err != nil {
			return fmt.Errorf("%w: node payload: %v", ErrInvalidPatch, err)
		}
		*node = next
		return nil
	}
	switch suffix[0] {
	case "props":
		node.Props = cloneRaw(value)
	case "metadata":
		if len(suffix) != 2 {
			return fmt.Errorf("%w: metadata replacement requires one key", ErrInvalidPatch)
		}
		if node.Metadata == nil {
			node.Metadata = map[string]json.RawMessage{}
		}
		node.Metadata[suffix[1]] = cloneRaw(value)
	case "children":
		if len(suffix) != 2 {
			return fmt.Errorf("%w: child replacement requires index", ErrInvalidPatch)
		}
		index, err := strconv.Atoi(suffix[1])
		if err != nil || index < 0 || index >= len(node.Children) {
			return fmt.Errorf("%w: invalid child index", ErrInvalidPatch)
		}
		var next component.Node
		if err := json.Unmarshal(value, &next); err != nil {
			return fmt.Errorf("%w: node payload: %v", ErrInvalidPatch, err)
		}
		node.Children[index] = next
	default:
		return fmt.Errorf("%w: unsupported replacement field %s", ErrInvalidPatch, suffix[0])
	}
	return nil
}

func removeValue(tree *component.Tree, path string) error {
	parts, err := parsePath(path)
	if err != nil {
		return err
	}
	if len(parts) >= 2 && (parts[0] == "id" || parts[0] == "key") && len(parts) == 2 {
		if tree.Root.ID == parts[1] || (parts[0] == "key" && tree.Root.Key == parts[1]) {
			return fmt.Errorf("%w: cannot remove root", ErrInvalidPatch)
		}
		if removeNode(&tree.Root, parts[0], parts[1]) {
			return nil
		}
		return fmt.Errorf("%w: node not found", ErrInvalidPatch)
	}
	node, suffix, err := locateNode(tree, path)
	if err != nil {
		return err
	}
	if len(suffix) >= 2 && suffix[0] == "children" {
		index, err := strconv.Atoi(suffix[1])
		if err != nil || index < 0 || index >= len(node.Children) {
			return fmt.Errorf("%w: invalid child index", ErrInvalidPatch)
		}
		node.Children = append(node.Children[:index], node.Children[index+1:]...)
		return nil
	}
	if len(suffix) == 2 && suffix[0] == "metadata" {
		delete(node.Metadata, suffix[1])
		return nil
	}
	return fmt.Errorf("%w: remove supports node, children, or metadata paths", ErrInvalidPatch)
}

func readRaw(tree *component.Tree, path string) (json.RawMessage, error) {
	node, suffix, err := locateNode(tree, path)
	if err != nil {
		return nil, err
	}
	var value any = *node
	if len(suffix) > 0 {
		switch suffix[0] {
		case "props":
			return cloneRaw(node.Props), nil
		case "metadata":
			if len(suffix) != 2 {
				return nil, fmt.Errorf("%w: metadata read requires key", ErrInvalidPatch)
			}
			return cloneRaw(node.Metadata[suffix[1]]), nil
		case "children":
			if len(suffix) != 2 {
				return nil, fmt.Errorf("%w: child read requires index", ErrInvalidPatch)
			}
			index, err := strconv.Atoi(suffix[1])
			if err != nil || index < 0 || index >= len(node.Children) {
				return nil, fmt.Errorf("%w: invalid child index", ErrInvalidPatch)
			}
			value = node.Children[index]
		default:
			return nil, fmt.Errorf("%w: unsupported read field %s", ErrInvalidPatch, suffix[0])
		}
	}
	data, err := json.Marshal(value)
	return data, err
}

func locateNode(tree *component.Tree, path string) (*component.Node, []string, error) {
	parts, err := parsePath(path)
	if err != nil {
		return nil, nil, err
	}
	if len(parts) == 0 || (len(parts) == 1 && parts[0] == "root") {
		return &tree.Root, nil, nil
	}
	if parts[0] == "root" {
		return walkByRootPath(&tree.Root, parts[1:])
	}
	if len(parts) >= 2 && parts[0] == "id" {
		node := findNode(&tree.Root, func(n component.Node) bool { return n.ID == parts[1] })
		if node == nil {
			return nil, nil, fmt.Errorf("%w: id %q not found", ErrInvalidPatch, parts[1])
		}
		return node, parts[2:], nil
	}
	if len(parts) >= 2 && parts[0] == "key" {
		node := findNode(&tree.Root, func(n component.Node) bool { return n.Key == parts[1] })
		if node == nil {
			return nil, nil, fmt.Errorf("%w: key %q not found", ErrInvalidPatch, parts[1])
		}
		return node, parts[2:], nil
	}
	return nil, nil, fmt.Errorf("%w: unsupported path %s", ErrInvalidPatch, path)
}

func walkByRootPath(node *component.Node, parts []string) (*component.Node, []string, error) {
	if len(parts) == 0 {
		return node, nil, nil
	}
	if parts[0] != "children" {
		return node, parts, nil
	}
	if len(parts) < 2 {
		return node, parts, nil
	}
	index, err := strconv.Atoi(parts[1])
	if err != nil || index < 0 || index >= len(node.Children) {
		return nil, nil, fmt.Errorf("%w: invalid child index", ErrInvalidPatch)
	}
	return walkByRootPath(&node.Children[index], parts[2:])
}

func findNode(node *component.Node, match func(component.Node) bool) *component.Node {
	if match(*node) {
		return node
	}
	for i := range node.Children {
		if found := findNode(&node.Children[i], match); found != nil {
			return found
		}
	}
	return nil
}

func removeNode(parent *component.Node, selector, value string) bool {
	for i := range parent.Children {
		child := parent.Children[i]
		if (selector == "id" && child.ID == value) || (selector == "key" && child.Key == value) {
			parent.Children = append(parent.Children[:i], parent.Children[i+1:]...)
			return true
		}
	}
	for i := range parent.Children {
		if removeNode(&parent.Children[i], selector, value) {
			return true
		}
	}
	return false
}

func parsePath(path string) ([]string, error) {
	if path == "" || path == "/" {
		return nil, nil
	}
	if !strings.HasPrefix(path, "/") {
		return nil, fmt.Errorf("%w: path must be absolute", ErrInvalidPatch)
	}
	raw := strings.Split(strings.TrimPrefix(path, "/"), "/")
	parts := make([]string, len(raw))
	for i, part := range raw {
		part = strings.ReplaceAll(part, "~1", "/")
		part = strings.ReplaceAll(part, "~0", "~")
		parts[i] = part
	}
	return parts, nil
}

func bindData(node *component.Node, raw json.RawMessage) error {
	var one string
	if err := json.Unmarshal(raw, &one); err == nil && one != "" {
		if !contains(node.DataBindings, one) {
			node.DataBindings = append(node.DataBindings, one)
		}
		return nil
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err != nil {
		return fmt.Errorf("%w: bindData expects string or []string", ErrInvalidPatch)
	}
	for _, binding := range many {
		if binding != "" && !contains(node.DataBindings, binding) {
			node.DataBindings = append(node.DataBindings, binding)
		}
	}
	return nil
}

func bindAction(node *component.Node, raw json.RawMessage) error {
	var bindings map[string]string
	if err := json.Unmarshal(raw, &bindings); err != nil {
		return fmt.Errorf("%w: bindAction expects object", ErrInvalidPatch)
	}
	if node.ActionBindings == nil {
		node.ActionBindings = map[string]string{}
	}
	for event, action := range bindings {
		if event != "" && action != "" {
			node.ActionBindings[event] = action
		}
	}
	return nil
}

func cloneRaw(raw json.RawMessage) json.RawMessage {
	if raw == nil {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}

func jsonEqual(a, b json.RawMessage) bool {
	var av any
	var bv any
	if len(a) == 0 {
		a = []byte("null")
	}
	if len(b) == 0 {
		b = []byte("null")
	}
	if json.Unmarshal(a, &av) != nil || json.Unmarshal(b, &bv) != nil {
		return bytes.Equal(bytes.TrimSpace(a), bytes.TrimSpace(b))
	}
	return reflect.DeepEqual(av, bv)
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
