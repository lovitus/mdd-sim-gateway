//go:build linux

package linuxmodem

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInterfaceCounterReadbackDistinguishesUnknownFromZero(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "class/net/wwan0/statistics")
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "rx_bytes"), []byte("0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := readInterfaceCounters(root, "wwan0"); ok {
		t.Fatal("partial counters reported complete")
	}
	if err := os.WriteFile(filepath.Join(path, "tx_bytes"), []byte("1234\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if rx, tx, ok := readInterfaceCounters(root, "wwan0"); !ok || rx != 0 || tx != 1234 {
		t.Fatalf("counters %d %d %v", rx, tx, ok)
	}
	if err := os.WriteFile(filepath.Join(path, "tx_bytes"), []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if rx, tx, ok := readInterfaceCounters(root, "wwan0"); ok || rx != 0 || tx != 0 {
		t.Fatal("invalid counters accepted")
	}
	for _, name := range []string{"", "..", "../wwan0", "/wwan0"} {
		if _, _, ok := readInterfaceCounters(root, name); ok {
			t.Fatal("invalid interface accepted")
		}
	}
}
