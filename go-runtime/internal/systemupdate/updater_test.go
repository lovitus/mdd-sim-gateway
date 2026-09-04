package systemupdate

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

type updateTransport struct{ archive []byte }

func (transport updateTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if strings.Contains(request.URL.Path, "/releases/tags/") {
		digest := sha256.Sum256(transport.archive)
		body := `{"tag_name":"v2.0.0","assets":[{"name":"mdd-2.0.0-linux-amd64.tar","browser_download_url":"https://download.invalid/release","digest":"sha256:` + hex.EncodeToString(digest[:]) + `","size":` + strconv.Itoa(len(transport.archive)) + `}]}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(transport.archive)), Header: make(http.Header)}, nil
}

func TestFetchAndStageVerifiesDigestAndRejectsUnsafeMembers(t *testing.T) {
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	if err := writer.WriteHeader(&tar.Header{Name: "mdd-2.0.0", Typeflag: tar.TypeDir, Mode: 0o700}); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteHeader(&tar.Header{Name: "mdd-2.0.0/manifest.json", Typeflag: tar.TypeReg, Mode: 0o600, Size: int64(len(`{"schema_version":3,"release_id":"mdd-2.0.0","source_revision":"abc","os":"linux","architecture":"amd64","artifacts":[]}`))}); err != nil {
		t.Fatal(err)
	}
	_, _ = writer.Write([]byte(`{"schema_version":3,"release_id":"mdd-2.0.0","source_revision":"abc","os":"linux","architecture":"amd64","artifacts":[]}`))
	_ = writer.Close()
	_, err := FetchAndStage(context.Background(), "example/project", "2.0.0", filepath.Join(t.TempDir(), "stage"), &http.Client{Transport: updateTransport{archive: archive.Bytes()}})
	if err == nil {
		t.Fatal("empty release manifest unexpectedly accepted")
	}
	if err := extractTar("/does/not/exist", t.TempDir()); err == nil {
		t.Fatal("missing archive unexpectedly extracted")
	}
}
