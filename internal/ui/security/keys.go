package security

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"hash"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nbaertsch/afterburner/internal/ui/protocol"
)

const (
	AlgorithmHMACSHA256 = "HMAC-SHA256"
	DefaultMaxSkew      = 5 * time.Minute
)

type RootSecret struct {
	value []byte
}

func NewRootSecret(value []byte) RootSecret {
	return RootSecret{value: append([]byte(nil), value...)}
}

func (s RootSecret) IsZero() bool { return len(s.value) == 0 }

func (s RootSecret) String() string { return "[redacted root secret]" }

func (s RootSecret) BytesForTest() []byte { return append([]byte(nil), s.value...) }

type ExtensionIdentity struct {
	SessionID   string
	ExtensionID string
	Protocol    string
}

type KeyRing struct {
	mu      sync.RWMutex
	current uint64
	roots   map[uint64]RootSecret
}

func NewKeyRing(currentEpoch uint64, root RootSecret) *KeyRing {
	ring := &KeyRing{current: currentEpoch, roots: map[uint64]RootSecret{}}
	if !root.IsZero() {
		ring.roots[currentEpoch] = root
	}
	return ring
}

func (r *KeyRing) Add(epoch uint64, root RootSecret) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.roots == nil {
		r.roots = map[uint64]RootSecret{}
	}
	r.roots[epoch] = root
	if epoch > r.current {
		r.current = epoch
	}
}

func (r *KeyRing) DropBefore(epoch uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for keyEpoch := range r.roots {
		if keyEpoch < epoch {
			delete(r.roots, keyEpoch)
		}
	}
}

func (r *KeyRing) CurrentEpoch() uint64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.current
}

func (r *KeyRing) Epochs() []uint64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	epochs := make([]uint64, 0, len(r.roots))
	for epoch := range r.roots {
		epochs = append(epochs, epoch)
	}
	sort.Slice(epochs, func(i, j int) bool { return epochs[i] < epochs[j] })
	return epochs
}

func (r *KeyRing) derive(epoch uint64, identity ExtensionIdentity) ([]byte, error) {
	if identity.ExtensionID == "" || identity.SessionID == "" {
		return nil, protocol.StructuredError{Code: protocol.ErrorAuthenticationFailed, Message: "extension identity is incomplete", Recoverable: false}
	}
	r.mu.RLock()
	root, ok := r.roots[epoch]
	r.mu.RUnlock()
	if !ok || root.IsZero() {
		return nil, protocol.StructuredError{Code: protocol.ErrorAuthenticationFailed, Message: "key epoch is not accepted", Recoverable: false, Target: "auth.epoch"}
	}
	return DeriveExtensionKey(root, identity, epoch), nil
}

func DeriveExtensionKey(root RootSecret, identity ExtensionIdentity, epoch uint64) []byte {
	identityProtocol := identity.Protocol
	if identityProtocol == "" {
		identityProtocol = protocol.Protocol
	}
	info := strings.Join([]string{protocol.Protocol, identityProtocol, identity.SessionID, identity.ExtensionID, strconv.FormatUint(epoch, 10)}, "\x00")
	salt := []byte("afterburner.ui.hkdf." + strconv.FormatUint(epoch, 10))
	prk := hkdfExtract(sha256.New, salt, root.value)
	return hkdfExpand(sha256.New, prk, []byte(info), sha256.Size)
}

type Signer struct {
	Ring        *KeyRing
	ReplayCache *NonceCache
	MaxSkew     time.Duration
	Now         func() time.Time
}

func (s Signer) Sign(envelope *protocol.Envelope, identity ExtensionIdentity) error {
	if envelope == nil {
		return protocol.StructuredError{Code: protocol.ErrorAuthenticationFailed, Message: "envelope is nil", Recoverable: false}
	}
	if s.Ring == nil {
		return protocol.StructuredError{Code: protocol.ErrorAuthenticationFailed, Message: "key ring is unavailable", Recoverable: false}
	}
	epoch := envelope.Epoch
	if epoch == 0 {
		epoch = s.Ring.CurrentEpoch()
		envelope.Epoch = epoch
	}
	nonce, err := RandomNonce()
	if err != nil {
		return protocol.StructuredError{Code: protocol.ErrorAuthenticationFailed, Message: "nonce generation failed", Recoverable: true}
	}
	now := s.now()
	envelope.Auth = &protocol.AuthHeader{Algorithm: AlgorithmHMACSHA256, KeyID: keyID(identity.ExtensionID, epoch), Epoch: epoch, Nonce: nonce, Timestamp: now}
	canonical, err := canonicalEnvelope(*envelope)
	if err != nil {
		return protocol.StructuredError{Code: protocol.ErrorAuthenticationFailed, Message: "canonical serialization failed", Recoverable: false}
	}
	key, err := s.Ring.derive(epoch, identity)
	if err != nil {
		return err
	}
	signature := hmacSHA256(key, canonical)
	envelope.Auth.Signature = base64.RawURLEncoding.EncodeToString(signature)
	return nil
}

func (s Signer) Verify(envelope protocol.Envelope, identity ExtensionIdentity) error {
	if s.Ring == nil {
		return protocol.StructuredError{Code: protocol.ErrorAuthenticationFailed, Message: "key ring is unavailable", Recoverable: false}
	}
	if envelope.Auth == nil || envelope.Auth.Signature == "" {
		return protocol.StructuredError{Code: protocol.ErrorAuthenticationFailed, Message: "missing authentication signature", Recoverable: false, Target: "auth.signature"}
	}
	if envelope.Auth.Algorithm != AlgorithmHMACSHA256 {
		return protocol.StructuredError{Code: protocol.ErrorAuthenticationFailed, Message: "unsupported authentication algorithm", Recoverable: false, Target: "auth.algorithm"}
	}
	maxSkew := s.MaxSkew
	if maxSkew == 0 {
		maxSkew = DefaultMaxSkew
	}
	now := s.now()
	if envelope.Auth.Timestamp.Before(now.Add(-maxSkew)) || envelope.Auth.Timestamp.After(now.Add(maxSkew)) {
		return protocol.StructuredError{Code: protocol.ErrorAuthenticationFailed, Message: "authentication timestamp is outside allowed skew", Recoverable: false, Target: "auth.timestamp"}
	}
	key, err := s.Ring.derive(envelope.Auth.Epoch, identity)
	if err != nil {
		return err
	}
	provided, err := base64.RawURLEncoding.DecodeString(envelope.Auth.Signature)
	if err != nil {
		return protocol.StructuredError{Code: protocol.ErrorAuthenticationFailed, Message: "authentication signature is malformed", Recoverable: false, Target: "auth.signature"}
	}
	canonical, err := canonicalEnvelope(envelope)
	if err != nil {
		return protocol.StructuredError{Code: protocol.ErrorAuthenticationFailed, Message: "canonical serialization failed", Recoverable: false}
	}
	if !hmac.Equal(provided, hmacSHA256(key, canonical)) {
		return protocol.StructuredError{Code: protocol.ErrorAuthenticationFailed, Message: "authentication signature mismatch", Recoverable: false, Target: "auth.signature"}
	}
	if s.ReplayCache != nil {
		if err := s.ReplayCache.Add(identity.ExtensionID+":"+envelope.Auth.Nonce, envelope.Auth.Timestamp); err != nil {
			return err
		}
	}
	return nil
}

func (s Signer) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func canonicalEnvelope(envelope protocol.Envelope) ([]byte, error) {
	if envelope.Auth != nil {
		auth := *envelope.Auth
		auth.Signature = ""
		envelope.Auth = &auth
	}
	return CanonicalJSON(envelope)
}

func hmacSHA256(key, payload []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(payload)
	return mac.Sum(nil)
}

func keyID(extensionID string, epoch uint64) string {
	return fmt.Sprintf("afterburner.ui:%s:%d", extensionID, epoch)
}

func RandomNonce() (string, error) {
	var raw [16]byte
	if _, err := io.ReadFull(rand.Reader, raw[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func hkdfExtract(hash func() hash.Hash, salt, secret []byte) []byte {
	if len(salt) == 0 {
		salt = make([]byte, hash().Size())
	}
	mac := hmac.New(hash, salt)
	mac.Write(secret)
	return mac.Sum(nil)
}

func hkdfExpand(hash func() hash.Hash, prk, info []byte, length int) []byte {
	var t []byte
	okm := make([]byte, 0, length)
	counter := byte(1)
	for len(okm) < length {
		mac := hmac.New(hash, prk)
		mac.Write(t)
		mac.Write(info)
		mac.Write([]byte{counter})
		t = mac.Sum(nil)
		okm = append(okm, t...)
		counter++
	}
	return okm[:length]
}
