package autoforward

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadStatusValidatesPublishedDocument(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mappings.json")
	now := time.Date(2026, 9, 14, 3, 0, 0, 0, time.UTC)
	store := NewStatusStore(path)
	store.processID = 42
	store.now = func() time.Time { return now }
	mapping := MappingStatus{Network: "tcp4", WindowsAddress: "127.0.0.1:18000", WSLAddress: "127.0.0.1:8000", State: "active"}
	if err := store.Publish("tcp", []MappingStatus{mapping}); err != nil {
		t.Fatal(err)
	}
	document, err := ReadStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if document.ProcessID != 42 || len(document.Mappings) != 1 || document.Mappings[0] != mapping {
		t.Fatalf("document: %#v", document)
	}
	if err := document.ValidateFresh(now.Add(time.Minute), DefaultStatusMaxAge); err != nil {
		t.Fatalf("fresh status rejected: %v", err)
	}
}

func TestStatusStoreRefreshesUnchangedHeartbeat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mappings.json")
	now := time.Date(2026, 9, 14, 3, 0, 0, 0, time.UTC)
	store := NewStatusStore(path)
	store.now = func() time.Time { return now }
	mapping := MappingStatus{Network: "udp4", WindowsAddress: "127.0.0.1:15353", WSLAddress: "127.0.0.1:5353", State: "active"}
	if err := store.Publish("udp", []MappingStatus{mapping}); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(StatusHeartbeatInterval / 2)
	if err := store.Publish("udp", []MappingStatus{mapping}); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(second) != string(first) {
		t.Fatal("status was rewritten before the heartbeat interval")
	}
	now = now.Add(StatusHeartbeatInterval)
	if err := store.Publish("udp", []MappingStatus{mapping}); err != nil {
		t.Fatal(err)
	}
	third, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(third) == string(first) {
		t.Fatal("status heartbeat did not refresh updated_at")
	}
}

func TestStatusStoreRejectsInvalidMapping(t *testing.T) {
	store := NewStatusStore(filepath.Join(t.TempDir(), "mappings.json"))
	err := store.Publish("tcp", []MappingStatus{{Network: "tcp4", WindowsAddress: "bad", WSLAddress: "127.0.0.1:8000", State: "active"}})
	if err == nil || !strings.Contains(err.Error(), "invalid Windows address") {
		t.Fatalf("Publish returned %v", err)
	}
}

func TestReadStatusRejectsInvalidDocuments(t *testing.T) {
	validTime := time.Now().UTC().Format(time.RFC3339Nano)
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"unknown field", `{"version":1,"process_id":1,"updated_at":"` + validTime + `","mappings":[],"extra":true}`, "unknown field"},
		{"unsupported version", `{"version":2,"process_id":1,"updated_at":"` + validTime + `","mappings":[]}`, "unsupported status version"},
		{"invalid process", `{"version":1,"process_id":0,"updated_at":"` + validTime + `","mappings":[]}`, "process_id"},
		{"invalid network", `{"version":1,"process_id":1,"updated_at":"` + validTime + `","mappings":[{"network":"raw","windows_address":"127.0.0.1:1","wsl_address":"127.0.0.1:1","state":"active"}]}`, "unsupported network"},
		{"invalid active metadata", `{"version":1,"process_id":1,"updated_at":"` + validTime + `","mappings":[{"network":"tcp4","windows_address":"127.0.0.1:1","wsl_address":"127.0.0.1:1","state":"active","error":"bad"}]}`, "active mapping"},
		{"trailing object", `{"version":1,"process_id":1,"updated_at":"` + validTime + `","mappings":[]} {}`, "exactly one"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "status.json")
			if err := os.WriteFile(path, []byte(test.content), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := ReadStatus(path)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ReadStatus returned %v", err)
			}
		})
	}
}

func TestReadStatusRejectsOversizedAndLinkedFiles(t *testing.T) {
	oversized := filepath.Join(t.TempDir(), "oversized.json")
	if err := os.WriteFile(oversized, make([]byte, MaxStatusDocumentSize+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadStatus(oversized); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized status returned %v", err)
	}

	directory := t.TempDir()
	target := filepath.Join(directory, "target.json")
	link := filepath.Join(directory, "link.json")
	if err := os.WriteFile(target, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		if errors.Is(err, os.ErrPermission) {
			t.Skip("symlink creation is not permitted")
		}
		t.Fatal(err)
	}
	if _, err := ReadStatus(link); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("linked status returned %v", err)
	}
}

func TestStatusDocumentFreshness(t *testing.T) {
	now := time.Date(2026, 9, 14, 3, 0, 0, 0, time.UTC)
	document := StatusDocument{UpdatedAt: now.Add(-DefaultStatusMaxAge - time.Second).Format(time.RFC3339Nano)}
	if err := document.ValidateFresh(now, DefaultStatusMaxAge); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale status returned %v", err)
	}
	document.UpdatedAt = now.Add(StatusHeartbeatInterval + time.Second).Format(time.RFC3339Nano)
	if err := document.ValidateFresh(now, DefaultStatusMaxAge); err == nil || !strings.Contains(err.Error(), "future") {
		t.Fatalf("future status returned %v", err)
	}
	if err := document.ValidateFresh(now, 0); err == nil {
		t.Fatal("non-positive maximum age was accepted")
	}
}
