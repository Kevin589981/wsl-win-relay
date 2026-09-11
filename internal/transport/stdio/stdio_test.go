package stdio

import (
	"bytes"
	"io"
	"testing"
)

func TestEndpointRoundTrip(t *testing.T) {
	var out bytes.Buffer
	e := New(bytes.NewBufferString("input"), &out, nil)
	b := make([]byte, 5)
	if _, err := io.ReadFull(e, b); err != nil {
		t.Fatal(err)
	}
	if string(b) != "input" {
		t.Fatalf("got %q", b)
	}
	if _, err := e.Write([]byte("output")); err != nil {
		t.Fatal(err)
	}
	if out.String() != "output" {
		t.Fatalf("got %q", out.String())
	}
}
