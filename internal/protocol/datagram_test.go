package protocol

import (
	"bytes"
	"testing"
)

func TestDatagramPayloadRoundTrip(t *testing.T) {
	wantData := []byte("dns")
	payload, err := EncodeDatagram("1.1.1.1:53", wantData)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, data, err := DecodeDatagram(payload)
	if err != nil {
		t.Fatal(err)
	}
	if endpoint != "1.1.1.1:53" || !bytes.Equal(data, wantData) {
		t.Fatalf("got %s %q", endpoint, data)
	}
}

func TestDecodeDatagramRejectsInvalidLength(t *testing.T) {
	if _, _, err := DecodeDatagram([]byte{0, 5, 'x'}); err == nil {
		t.Fatal("expected invalid endpoint error")
	}
}
