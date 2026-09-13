package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Kevin589981/wsl-win-relay/internal/autoforward"
	appconfig "github.com/Kevin589981/wsl-win-relay/internal/config"
)

func TestRunListsAndResolvesFreshStatus(t *testing.T) {
	now := time.Date(2026, 9, 14, 4, 0, 0, 0, time.UTC)
	path := writeStatus(t, autoforward.StatusDocument{
		Version: autoforward.StatusVersion, ProcessID: 123, UpdatedAt: now.Format(time.RFC3339Nano),
		Mappings: []autoforward.MappingStatus{{Network: "tcp4", WindowsAddress: "127.0.0.1:18000", WSLAddress: "127.0.0.1:8000", State: "active"}},
	})
	alive := func(processID int) (bool, error) { return processID == 123, nil }
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-file", path}, &stdout, &stderr, func() time.Time { return now }, alive); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "127.0.0.1:18000") || !strings.Contains(stdout.String(), "127.0.0.1:8000") {
		t.Fatalf("output=%q", stdout.String())
	}
	stdout.Reset()
	stderr.Reset()
	args := []string{"-file", path, "-resolve-network", "tcp4", "-resolve-wsl", "127.0.0.1:8000"}
	if code := run(args, &stdout, &stderr, func() time.Time { return now }, alive); code != 0 || stdout.String() != "127.0.0.1:18000\n" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestRunEmitsValidatedJSON(t *testing.T) {
	now := time.Now().UTC()
	want := autoforward.StatusDocument{Version: autoforward.StatusVersion, ProcessID: 7, UpdatedAt: now.Format(time.RFC3339Nano), Mappings: []autoforward.MappingStatus{}}
	path := writeStatus(t, want)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-file", path, "-json"}, &stdout, &stderr, func() time.Time { return now }, func(int) (bool, error) { return true, nil }); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	var got autoforward.StatusDocument
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ProcessID != want.ProcessID || got.UpdatedAt != want.UpdatedAt {
		t.Fatalf("document=%#v", got)
	}
}

func TestRunRejectsStaleDeadAndRejectedMappings(t *testing.T) {
	now := time.Date(2026, 9, 14, 4, 0, 0, 0, time.UTC)
	stale := writeStatus(t, autoforward.StatusDocument{Version: 1, ProcessID: 1, UpdatedAt: now.Add(-3 * time.Minute).Format(time.RFC3339Nano), Mappings: []autoforward.MappingStatus{}})
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-file", stale}, &stdout, &stderr, func() time.Time { return now }, func(int) (bool, error) { return true, nil }); code != 1 || !strings.Contains(stderr.String(), "stale") {
		t.Fatalf("stale code=%d stderr=%q", code, stderr.String())
	}
	fresh := writeStatus(t, autoforward.StatusDocument{Version: 1, ProcessID: 2, UpdatedAt: now.Format(time.RFC3339Nano), Mappings: []autoforward.MappingStatus{}})
	stderr.Reset()
	if code := run([]string{"-file", fresh}, &stdout, &stderr, func() time.Time { return now }, func(int) (bool, error) { return false, nil }); code != 1 || !strings.Contains(stderr.String(), "not running") {
		t.Fatalf("dead code=%d stderr=%q", code, stderr.String())
	}
	rejected := writeStatus(t, autoforward.StatusDocument{
		Version: 1, ProcessID: 3, UpdatedAt: now.Format(time.RFC3339Nano),
		Mappings: []autoforward.MappingStatus{{Network: "tcp4", WindowsAddress: "127.0.0.1:0", WSLAddress: "127.0.0.1:8000", State: "rejected", Error: "address in use"}},
	})
	stderr.Reset()
	args := []string{"-file", rejected, "-resolve-network", "tcp4", "-resolve-wsl", "127.0.0.1:8000"}
	if code := run(args, &stdout, &stderr, func() time.Time { return now }, func(int) (bool, error) { return true, nil }); code != 1 || !strings.Contains(stderr.String(), "address in use") {
		t.Fatalf("rejected code=%d stderr=%q", code, stderr.String())
	}
}

func TestStatusPathLoadsConfiguredFile(t *testing.T) {
	statusFile := filepath.Join(t.TempDir(), "mapping status.json")
	config := appconfig.Default()
	config.AutoForward.StatusFile = statusFile
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := statusPath(options{config: configPath})
	if err != nil || got != statusFile {
		t.Fatalf("path=%q err=%v", got, err)
	}
}

func TestParseOptionsRejectsConflicts(t *testing.T) {
	for _, args := range [][]string{
		{"-max-age=0"},
		{"-resolve-network", "tcp4"},
		{"-resolve-wsl", "127.0.0.1:8000"},
		{"-json", "-resolve-network", "tcp4", "-resolve-wsl", "127.0.0.1:8000"},
		{"unexpected"},
	} {
		if _, err := parseOptions(args); err == nil {
			t.Fatalf("arguments accepted: %v", args)
		}
	}
}

func writeStatus(t *testing.T, document autoforward.StatusDocument) string {
	t.Helper()
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "status.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
