package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Kevin589981/wsl-win-relay/internal/transport/localipc"
)

func TestEqualTokenHex(t *testing.T) {
	if !equalTokenHex("AABBcc", "aabbCC") {
		t.Fatal("hex token comparison should ignore letter case")
	}
	for _, pair := range [][2]string{{"", "aabb"}, {"aabb", "aabb00"}, {"zz", "aabb"}} {
		if equalTokenHex(pair[0], pair[1]) {
			t.Fatalf("token pair %q/%q should not match", pair[0], pair[1])
		}
	}
}

func TestServeWorkerControlRequiresToken(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stopOnce sync.Once
	stopped := make(chan struct{})
	go serveWorkerControl(ctx, listener, func() {
		stopOnce.Do(func() {
			close(stopped)
			cancel()
		})
	}, "aabbcc")

	request := func(line string) string {
		dialCtx, dialCancel := context.WithTimeout(ctx, time.Second)
		defer dialCancel()
		var dialer net.Dialer
		conn, dialErr := dialer.DialContext(dialCtx, "tcp", listener.Addr().String())
		if dialErr != nil {
			t.Fatal(dialErr)
		}
		defer conn.Close()
		if _, err := conn.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		response, readErr := bufio.NewReader(conn).ReadString('\n')
		if readErr != nil {
			t.Fatal(readErr)
		}
		return strings.TrimSpace(response)
	}

	if got := request("PING deadbeef\n"); got != "ERR" {
		t.Fatalf("wrong-token probe response %q", got)
	}
	if got := request("PING AABBCC\n"); got != "PONG" {
		t.Fatalf("correct-token probe response %q", got)
	}
	if got := request("STOP\n"); got != "OK" {
		t.Fatalf("stop response %q", got)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("STOP did not invoke lifecycle cancellation")
	}
}

func TestProbeRoleClassifiesMismatchAndStopsConflict(t *testing.T) {
	endpoint := fmt.Sprintf("wsl-win-relay-role-test-%d", time.Now().UnixNano())
	listener, err := localipc.Listen(deriveEndpoint(endpoint, "control"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stopOnce sync.Once
	stopped := make(chan struct{})
	go serveWorkerControl(ctx, listener, func() {
		stopOnce.Do(func() {
			close(stopped)
			cancel()
		})
	}, "aabbcc")

	probeCtx, probeCancel := context.WithTimeout(ctx, time.Second)
	err = probeRole(probeCtx, endpoint, "deadbeef")
	probeCancel()
	if !errors.Is(err, errRoleTokenMismatch) {
		t.Fatalf("wrong-token probe error %v, want token mismatch", err)
	}
	probeCtx, probeCancel = context.WithTimeout(ctx, time.Second)
	err = probeRole(probeCtx, endpoint, "AABBCC")
	probeCancel()
	if err != nil {
		t.Fatalf("correct-token probe: %v", err)
	}
	logger := log.New(io.Discard, "", 0)
	if err := stopConflictingRole(ctx, endpoint, logger); err != nil {
		t.Fatalf("stop conflicting role: %v", err)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("conflicting role did not stop")
	}
}
