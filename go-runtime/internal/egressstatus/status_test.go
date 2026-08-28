package egressstatus

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAndResolveHostLoopbackProxy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proxy-status.json")
	payload := `{"desired_generation":"abc","exits":{"GB":{"ready":true,"proxy_host":"172.17.0.1","host_proxy_host":"127.0.0.1","proxy_port":22157,"node":"not-consumed"}}}`
	if err := os.WriteFile(path, []byte(payload), 0o644); err != nil {
		t.Fatal(err)
	}
	snapshot, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	proxyURL, err := snapshot.ProxyURL("gb")
	if err != nil || proxyURL != "socks5://127.0.0.1:22157" {
		t.Fatalf("proxy=%q err=%v", proxyURL, err)
	}
}

func TestResolveFailsClosedWithoutReadyHostLoopback(t *testing.T) {
	for name, exit := range map[string]Exit{
		"not-ready":       {Ready: false, HostProxyHost: "127.0.0.1", ProxyPort: 22157},
		"old-docker-only": {Ready: true, HostProxyHost: "", ProxyPort: 22157},
		"non-loopback":    {Ready: true, HostProxyHost: "172.17.0.1", ProxyPort: 22157},
		"bad-port":        {Ready: true, HostProxyHost: "127.0.0.1", ProxyPort: 0},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := (Snapshot{Exits: map[string]Exit{"gb": exit}}).ProxyURL("gb")
			if err == nil {
				t.Fatal("unsafe exit was accepted")
			}
		})
	}
	_, err := (Snapshot{Exits: map[string]Exit{}}).ProxyURL("gb")
	if err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("missing exit error=%v", err)
	}
}

func TestLoadRejectsSymlinkAndTrailingJSON(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target.json")
	if err := os.WriteFile(target, []byte(`{"exits":{"gb":{"ready":true}}} {}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(target); err == nil {
		t.Fatal("trailing JSON was accepted")
	}
	link := filepath.Join(directory, "link.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(link); err == nil {
		t.Fatal("symlink status was accepted")
	}
}
