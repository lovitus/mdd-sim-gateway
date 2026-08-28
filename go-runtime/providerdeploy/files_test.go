package providerdeploy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/providerconfig"
)

func TestSwitchLinkAtomicallyMovesBetweenValidatedDirectories(t *testing.T) {
	root := t.TempDir()
	first := testProviderDirectory(t, filepath.Join(root, "first"), 1)
	second := testProviderDirectory(t, filepath.Join(root, "second"), 2)
	link := filepath.Join(root, "current")
	if err := SwitchLink(link, first); err != nil {
		t.Fatal(err)
	}
	if target, err := CurrentTarget(link); err != nil || target != first {
		t.Fatalf("first target=%q err=%v", target, err)
	}
	if err := SwitchLink(link, second); err != nil {
		t.Fatal(err)
	}
	if target, err := CurrentTarget(link); err != nil || target != second {
		t.Fatalf("second target=%q err=%v", target, err)
	}
	if err := RemoveLink(link); err != nil {
		t.Fatal(err)
	}
	if target, err := CurrentTarget(link); err != nil || target != "" {
		t.Fatalf("removed target=%q err=%v", target, err)
	}
}

func testProviderDirectory(t *testing.T, path string, revision uint64) string {
	t.Helper()
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := providerconfig.Manifest{SchemaVersion: 1, CatalogRevision: revision, Providers: []providerconfig.ManifestEntry{}}
	payload, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(path, "manifest.json"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
