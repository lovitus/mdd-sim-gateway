//go:build !windows

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressexec"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/scopedtoken"
)

// Substitute only the external proxy executable. The helper, executor,
// publication, process replacement and confirmation paths remain real.
func init() {
	if os.Getenv("MDD_TEST_EGRESS_LISTENER") != "1" {
		return
	}
	if len(os.Args) != 4 || os.Args[2] != "-c" {
		os.Exit(2)
	}
	var config struct {
		Inbounds []struct {
			Listen string `json:"listen"`
			Port   int    `json:"listen_port"`
		} `json:"inbounds"`
	}
	payload, err := os.ReadFile(os.Args[3])
	if err != nil || json.Unmarshal(payload, &config) != nil {
		os.Exit(2)
	}
	if os.Args[1] == "check" {
		if _, err := os.Stat(filepath.Join(filepath.Dir(os.Args[3]), "reject-check")); err == nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	if os.Args[1] != "run" {
		os.Exit(2)
	}
	var listeners []net.Listener
	for _, inbound := range config.Inbounds {
		listener, err := net.Listen("tcp", net.JoinHostPort(inbound.Listen, fmt.Sprint(inbound.Port)))
		if err != nil {
			os.Exit(2)
		}
		listeners = append(listeners, listener)
		go func() {
			for {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				_ = conn.Close()
			}
		}()
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	<-ctx.Done()
	stop()
	for _, listener := range listeners {
		_ = listener.Close()
	}
	os.Exit(0)
}

func startEgressFixture(t *testing.T, root, desired, status, coreURL string) func() {
	t.Helper()
	t.Setenv("MDD_TEST_EGRESS_LISTENER", "1")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	base := listener.Addr().(*net.TCPAddr).Port - 157
	_ = listener.Close()
	tokenPath := filepath.Join(root, "token")
	if _, err := scopedtoken.Ensure(tokenPath); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- egressexec.Run(ctx, egressexec.Settings{
			DesiredPath: desired, StatusPath: status, StateDir: root, SingBoxPath: binary,
			CoreURL: coreURL, TokenPath: tokenPath, PortBase: base, Poll: 100 * time.Millisecond,
		})
	}()
	stopped := false
	finish := func() {
		if !stopped {
			stopped = true
			cancel()
			if err := <-done; err != nil {
				t.Error(err)
			}
		}
	}
	t.Cleanup(finish)
	return finish
}
