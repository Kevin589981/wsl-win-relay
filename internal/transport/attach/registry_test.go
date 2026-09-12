package attach

import (
	"bytes"
	"errors"
	"sync"
	"testing"
)

func TestNewRegistryCreatesPrivateToken(t *testing.T) {
	first, err := New()
	if err != nil {
		t.Fatal(err)
	}
	second, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Token()) != tokenSize || bytes.Equal(first.Token(), second.Token()) {
		t.Fatal("registry tokens were not independently generated")
	}
	if first.InstanceID() == 0 || first.InstanceID() == second.InstanceID() {
		t.Fatal("registries were not assigned independent instance IDs")
	}
	value := first.Token()
	value[0] ^= 0xff
	if bytes.Equal(value, first.Token()) {
		t.Fatal("Token returned internal storage")
	}
}

func TestAttachRequiresTokenAndAdvancesEpoch(t *testing.T) {
	registry, err := NewWithToken([]byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Attach([]byte("wrong")); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("err=%v", err)
	}
	first, err := registry.Attach([]byte("secret"))
	if err != nil || first.Epoch() != 1 || !first.Current() {
		t.Fatalf("first=%v err=%v current=%v", first, err, first != nil && first.Current())
	}
	second, err := registry.Attach([]byte("secret"))
	if err != nil || second.Epoch() != 2 || !second.Current() || first.Current() {
		t.Fatalf("second=%v err=%v first-current=%v", second, err, first.Current())
	}
}

func TestStaleDetachCannotReleaseNewAttachment(t *testing.T) {
	registry, err := NewWithToken([]byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	first, _ := registry.Attach([]byte("secret"))
	second, _ := registry.Attach([]byte("secret"))
	if err := first.Detach(); !errors.Is(err, ErrStaleAttachment) {
		t.Fatalf("stale detach err=%v", err)
	}
	if !second.Current() || registry.CurrentEpoch() != second.Epoch() {
		t.Fatal("stale detach released current attachment")
	}
	if err := second.Detach(); err != nil || second.Current() || registry.CurrentEpoch() != 0 {
		t.Fatalf("current detach err=%v epoch=%d", err, registry.CurrentEpoch())
	}
	if err := second.Detach(); err != nil {
		t.Fatalf("repeated detach err=%v", err)
	}
}

func TestCloseInvalidatesAttachmentAndRejectsFutureAttach(t *testing.T) {
	registry, err := NewWithToken([]byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	attachment, _ := registry.Attach([]byte("secret"))
	registry.Close()
	if attachment.Current() {
		t.Fatal("attachment remained current after registry close")
	}
	if _, err := registry.Attach([]byte("secret")); !errors.Is(err, ErrRegistryClosed) {
		t.Fatalf("attach after close err=%v", err)
	}
	if err := attachment.Detach(); !errors.Is(err, ErrStaleAttachment) {
		t.Fatalf("detach after close err=%v", err)
	}
}

func TestAttachRegistryConcurrentReplacement(t *testing.T) {
	registry, err := NewWithToken([]byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	const count = 32
	attachments := make([]*Attachment, count)
	var group sync.WaitGroup
	for index := range attachments {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			attachments[index], _ = registry.Attach([]byte("secret"))
		}(index)
	}
	group.Wait()
	current := registry.CurrentEpoch()
	if current == 0 {
		t.Fatal("concurrent attaches left no current generation")
	}
	for _, attachment := range attachments {
		if attachment == nil {
			t.Fatal("concurrent attach returned nil")
		}
	}
	owners := 0
	for _, attachment := range attachments {
		if attachment.Current() {
			owners++
		}
	}
	if owners != 1 {
		t.Fatalf("current attachment count=%d epoch=%d", owners, current)
	}
}
