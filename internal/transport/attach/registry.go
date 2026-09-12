// Package attach contains the generation-safe ownership state used by a
// reconnectable relay transport. It deliberately does not perform I/O; broker
// and connector implementations can layer their transport on these rules.
package attach

import (
	"crypto/hmac"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"sync"
)

const tokenSize = 32

var (
	ErrInvalidToken     = errors.New("invalid attach token")
	ErrRegistryClosed   = errors.New("attach registry is closed")
	ErrStaleAttachment  = errors.New("attachment is stale")
	ErrEmptyAttachToken = errors.New("attach token must not be empty")
)

// Registry authenticates connector attaches and tracks at most one current
// attachment. Replacing an attachment invalidates the old generation without
// allowing a delayed cleanup from that generation to affect the new one.
type Registry struct {
	mu         sync.Mutex
	token      []byte
	instanceID uint64
	epoch      uint64
	current    *Attachment
	closed     bool
}

// New creates a registry with a cryptographically random per-instance token.
func New() (*Registry, error) {
	token := make([]byte, tokenSize)
	if _, err := rand.Read(token); err != nil {
		return nil, err
	}
	return NewWithToken(token)
}

// NewWithToken creates a registry using token as its authentication secret.
// The token is copied, so callers may safely reuse or zero their input.
func NewWithToken(token []byte) (*Registry, error) {
	if len(token) == 0 {
		return nil, ErrEmptyAttachToken
	}
	var identity [8]byte
	if _, err := rand.Read(identity[:]); err != nil {
		return nil, err
	}
	instanceID := binary.BigEndian.Uint64(identity[:])
	if instanceID == 0 {
		instanceID = 1
	}
	return &Registry{token: append([]byte(nil), token...), instanceID: instanceID}, nil
}

// Token returns a copy of the registry token for the broker's attach
// handshake. Callers should keep it private to the per-user IPC channel.
func (r *Registry) Token() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]byte(nil), r.token...)
}

// InstanceID identifies one broker process lifetime. It remains stable while
// connectors are replaced and changes when a new registry is created after a
// broker restart.
func (r *Registry) InstanceID() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.instanceID
}

// Attach authenticates token and installs a new current generation.
func (r *Registry) Attach(token []byte) (*Attachment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, ErrRegistryClosed
	}
	if len(token) == 0 || !hmac.Equal(token, r.token) {
		return nil, ErrInvalidToken
	}
	r.epoch++
	if r.epoch == 0 {
		// Epoch wrap is practically unreachable, but never issue zero as a
		// valid generation because it is useful as an absent-value sentinel.
		r.epoch++
	}
	attachment := &Attachment{registry: r, epoch: r.epoch}
	r.current = attachment
	return attachment, nil
}

// Close permanently rejects new attaches and invalidates the current one.
func (r *Registry) Close() {
	r.mu.Lock()
	r.closed = true
	r.current = nil
	r.mu.Unlock()
}

// CurrentEpoch returns the active epoch, or zero when no connector is
// attached. It is intended for diagnostics and tests; it does not grant
// ownership.
func (r *Registry) CurrentEpoch() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.current == nil {
		return 0
	}
	return r.current.epoch
}

// Attachment is a single authenticated connector generation.
type Attachment struct {
	registry *Registry
	epoch    uint64
	once     sync.Once
	err      error
}

// Epoch identifies this attachment generation.
func (a *Attachment) Epoch() uint64 { return a.epoch }

// Current reports whether this attachment still owns the registry.
func (a *Attachment) Current() bool {
	a.registry.mu.Lock()
	defer a.registry.mu.Unlock()
	return !a.registry.closed && a.registry.current == a
}

// Detach releases this generation. A stale attachment cannot detach a newer
// generation; repeated calls on the same attachment are idempotent.
func (a *Attachment) Detach() error {
	a.once.Do(func() {
		a.registry.mu.Lock()
		defer a.registry.mu.Unlock()
		if a.registry.current != a {
			a.err = ErrStaleAttachment
			return
		}
		a.registry.current = nil
	})
	return a.err
}
