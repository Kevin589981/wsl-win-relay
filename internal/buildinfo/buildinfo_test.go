package buildinfo

import (
	"bytes"
	"strings"
	"testing"
)

func TestPrintRequested(t *testing.T) {
	oldVersion, oldCommit, oldBuiltAt := Version, Commit, BuiltAt
	defer func() { Version, Commit, BuiltAt = oldVersion, oldCommit, oldBuiltAt }()
	Version, Commit, BuiltAt = "v1.2.3", "abc123", "2026-09-14T00:00:00Z"
	var output bytes.Buffer
	if !PrintRequested(&output, "wsl-proxy", []string{"--version"}) {
		t.Fatal("version request was not handled")
	}
	for _, want := range []string{"wsl-proxy", "v1.2.3", "abc123", "2026-09-14T00:00:00Z"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("output %q does not contain %q", output.String(), want)
		}
	}
	if PrintRequested(&output, "wsl-proxy", []string{"--version", "extra"}) {
		t.Fatal("version request with extra arguments was accepted")
	}
}
