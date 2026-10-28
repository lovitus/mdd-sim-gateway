//go:build updater_stage_qualification

package systemupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/releasebundle"
)

// Temporary hosted cross-check, removed after qualification. The immutable
// published archive is input data only; no packaged program or service runs.
func TestPublishedReleaseStageQualification(t *testing.T) {
	archive, err := os.ReadFile(os.Getenv("MDD_QUALIFICATION_RELEASE_TAR"))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(archive)
	if hex.EncodeToString(digest[:]) != "503360cf9c3a35c3bc1a85414b4e1b5d1a7757143ee977722425210cc7f4bd12" {
		t.Fatal("published v2.1.0 archive identity mismatch")
	}
	staged, err := FetchAndStage(context.Background(), "example/project", "2.1.0", filepath.Join(t.TempDir(), "stage"), &http.Client{Transport: updateTransport{archive: archive, version: "2.1.0"}})
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := releasebundle.LoadDirectory(staged)
	if err != nil || manifest.SourceRevision != "90e9881a8551fa36b23e52f0aa01ef070f752bf1" || manifest.ReleaseID != "mdd-90e9881a8551" {
		t.Fatalf("published staged identity/contents invalid: manifest=%+v err=%v", manifest, err)
	}
	t.Logf("published v2.1.0 archive sha256=%x source=%s artifacts=%d strictly staged; no install/preflight/service execution", digest, manifest.SourceRevision, len(manifest.Artifacts))
}
