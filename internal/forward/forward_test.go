package forward

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
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

func TestParseMappingRejectsMalformedEndpoints(t *testing.T) {
	for _, value := range []string{"a=b", "127.0.0.1:0=127.0.0.1:80", "127.0.0.1:80=127.0.0.1", "127.0.0.1:80=[::1]:0"} {
		if _, err := ParseMapping(value); err == nil {
			t.Fatalf("expected %q to be rejected", value)
		}
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

func TestOpenDatagramAllRollsBackOnFailure(t *testing.T) {
	opener := &fakeDatagramOpener{failAt: 2}
	_, err := OpenDatagramAll(context.Background(), opener, []Mapping{{Windows: "a", WSL: "x"}, {Windows: "b", WSL: "y"}, {Windows: "c", WSL: "z"}})
	if err == nil {
		t.Fatal("expected registration failure")
	}
	if !reflect.DeepEqual(opener.closed, []string{"b", "a"}) {
		t.Fatalf("rollback order: %v", opener.closed)
	}
}

func TestOpenAllRejectsNilCloser(t *testing.T) {
	_, err := OpenAll(context.Background(), nilOpener{}, []Mapping{{Windows: "a", WSL: "x"}})
	if err == nil || !strings.Contains(err.Error(), "opener returned nil mapping") {
		t.Fatalf("err=%v", err)
	}
}

func TestOpenAllRejectsNilOpener(t *testing.T) {
	_, err := OpenAll(context.Background(), nil, []Mapping{{Windows: "a", WSL: "x"}})
	if err == nil || !strings.Contains(err.Error(), "opener is required") {
		t.Fatalf("err=%v", err)
	}
}

func TestOpenDatagramAllRejectsNilCloser(t *testing.T) {
	_, err := OpenDatagramAll(context.Background(), nilDatagramOpener{}, []Mapping{{Windows: "a", WSL: "x"}})
	if err == nil || !strings.Contains(err.Error(), "opener returned nil mapping") {
		t.Fatalf("err=%v", err)
	}
}

func TestOpenDatagramAllRejectsNilOpener(t *testing.T) {
	_, err := OpenDatagramAll(context.Background(), nil, []Mapping{{Windows: "a", WSL: "x"}})
	if err == nil || !strings.Contains(err.Error(), "opener is required") {
		t.Fatalf("err=%v", err)
	}
}

func TestOpenAllClosesHandleReturnedWithError(t *testing.T) {
	opener := &partialFailureOpener{}
	_, err := OpenAll(context.Background(), opener, []Mapping{{Windows: "a", WSL: "x"}})
	if err == nil {
		t.Fatal("expected registration failure")
	}
	if !opener.closed {
		t.Fatal("partially-created mapping was not closed")
	}
}

func TestOpenDatagramAllClosesHandleReturnedWithError(t *testing.T) {
	opener := &partialFailureDatagramOpener{}
	_, err := OpenDatagramAll(context.Background(), opener, []Mapping{{Windows: "a", WSL: "x"}})
	if err == nil {
		t.Fatal("expected registration failure")
	}
	if !opener.closed {
		t.Fatal("partially-created datagram mapping was not closed")
	}
}

type fakeOpener struct {
	calls  int
	failAt int
	closed []string
}

type fakeDatagramOpener struct {
	calls  int
	failAt int
	closed []string
}

type partialFailureOpener struct{ closed bool }

func (o *partialFailureOpener) ReverseForward(context.Context, string, string) (io.Closer, error) {
	return closerFunc(func() error { o.closed = true; return nil }), errors.New("setup failed")
}

type partialFailureDatagramOpener struct{ closed bool }

func (o *partialFailureDatagramOpener) ReverseDatagramForward(context.Context, string, string) (io.Closer, error) {
	return closerFunc(func() error { o.closed = true; return nil }), errors.New("setup failed")
}

type nilOpener struct{}

func (nilOpener) ReverseForward(context.Context, string, string) (io.Closer, error) {
	return nil, nil
}

type nilDatagramOpener struct{}

func (nilDatagramOpener) ReverseDatagramForward(context.Context, string, string) (io.Closer, error) {
	return nil, nil
}

func (f *fakeDatagramOpener) ReverseDatagramForward(_ context.Context, windows, _ string) (io.Closer, error) {
	f.calls++
	if f.calls > f.failAt {
		return nil, errors.New("rejected")
	}
	return closerFunc(func() error { f.closed = append(f.closed, windows); return nil }), nil
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
