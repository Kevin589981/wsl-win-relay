package main

import (
	"bufio"
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
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
	go serveWorkerControl(ctx, listener, func() { stopOnce.Do(func() { close(stopped) }) }, "aabbcc")

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
