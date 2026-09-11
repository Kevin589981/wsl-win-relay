package forward

import (
	"context"
	"errors"
	"io"
	"reflect"
	"testing"
)

func TestMappingsAcceptRepeatedValues(t *testing.T) {
	var got Mappings
	if err := got.Set("127.0.0.1:8000=127.0.0.1:80"); err != nil {
		t.Fatal(err)
	}
	if err := got.Set("[::1]:9000=[::1]:90"); err != nil {
		t.Fatal(err)
	}
	want := Mappings{{Windows: "127.0.0.1:8000", WSL: "127.0.0.1:80"}, {Windows: "[::1]:9000", WSL: "[::1]:90"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestOpenAllRollsBackOnFailure(t *testing.T) {
	opener := &fakeOpener{failAt: 2}
	_, err := OpenAll(context.Background(), opener, []Mapping{{Windows: "a", WSL: "x"}, {Windows: "b", WSL: "y"}, {Windows: "c", WSL: "z"}})
	if err == nil {
		t.Fatal("expected registration failure")
	}
	if !reflect.DeepEqual(opener.closed, []string{"b", "a"}) {
		t.Fatalf("rollback order: %v", opener.closed)
	}
}

type fakeOpener struct {
	calls  int
	failAt int
	closed []string
}

func (f *fakeOpener) ReverseForward(_ context.Context, windows, _ string) (io.Closer, error) {
	f.calls++
	if f.calls > f.failAt {
		return nil, errors.New("rejected")
	}
	return closerFunc(func() error { f.closed = append(f.closed, windows); return nil }), nil
}

type closerFunc func() error

func (f closerFunc) Close() error { return f() }
