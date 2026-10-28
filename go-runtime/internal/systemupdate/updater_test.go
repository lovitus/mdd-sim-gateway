package systemupdate

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/releasebundle"
)

// Only the external release lookup and download are substituted. Staging and
// manifest validation run unchanged against actual files.
type updateTransport struct {
	archive []byte
	version string
	asset   func(*releaseAsset)
}

func (transport updateTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	version := transport.version
	if version == "" {
		version = "2.0.0"
	}
	var body []byte
	switch request.URL.String() {
	case "https://api.github.com/repos/example/project/releases/tags/v" + version:
		digest := sha256.Sum256(transport.archive)
		asset := releaseAsset{Name: "mdd-" + version + "-linux-amd64.tar", URL: "https://download.invalid/release", Digest: "sha256:" + hex.EncodeToString(digest[:]), Size: int64(len(transport.archive))}
		if transport.asset != nil {
			transport.asset(&asset)
		}
		var err error
		body, err = json.Marshal(releaseAPI{Tag: "v" + version, Assets: []releaseAsset{asset}})
		if err != nil {
			return nil, err
		}
	case "https://download.invalid/release":
		body = transport.archive
	default:
		return nil, fmt.Errorf("unexpected fixture request: %s", request.URL)
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header)}, nil
}

type stageMember struct {
	header tar.Header
	body   []byte
}

func validStageMembers(t *testing.T) ([]stageMember, releasebundle.Manifest) {
	t.Helper()
	root := t.TempDir()
	var inputs []releasebundle.Input
	for _, item := range []struct {
		role string
		mode os.FileMode
	}{
		{releasebundle.RoleCore, 0o755}, {releasebundle.RoleAgent, 0o755},
		{releasebundle.RoleAgentAudio, 0o755}, {releasebundle.RoleProvider, 0o755},
		{releasebundle.RoleUpdater, 0o755}, {releasebundle.RoleXray, 0o755},
		{releasebundle.RoleCoreUnit, 0o644}, {releasebundle.RoleAgentUnit, 0o644},
		{releasebundle.RoleProviderUnit, 0o644}, {releasebundle.RoleApplyUnit, 0o644},
		{releasebundle.RoleEgressUnit, 0o644}, {releasebundle.RoleGuardUnit, 0o644},
		{releasebundle.RoleUpdaterUnit, 0o644}, {releasebundle.RoleUpdaterPath, 0o644},
		{releasebundle.RoleProviderSource, 0o644}, {releasebundle.RoleProviderNotice, 0o644},
		{releasebundle.RoleXraySource, 0o644}, {releasebundle.RoleXrayNotice, 0o644},
		{releasebundle.RoleProjectLicense, 0o644}, {releasebundle.RoleProjectNotice, 0o644},
		{releasebundle.RoleThirdParty, 0o644}, {releasebundle.RoleGoLicenses, 0o644},
	} {
		path := filepath.Join(root, "input-"+item.role)
		if err := os.WriteFile(path, []byte("fixture content for "+item.role+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		input := releasebundle.Input{Name: item.role, Role: item.role, Mode: item.mode, SourcePath: path}
		if item.mode == 0o755 {
			input.GoVersion = runtime.Version()
		}
		inputs = append(inputs, input)
	}
	directory := filepath.Join(root, "mdd-stage-test")
	manifest, err := releasebundle.CreateDirectory(directory, releasebundle.Manifest{
		ReleaseID: "mdd-stage-test", SourceRevision: strings.Repeat("a", 40), OS: "linux", Architecture: "amd64",
	}, inputs)
	if err != nil {
		t.Fatal("valid release fixture creation:", err)
	}
	if loaded, err := releasebundle.LoadDirectory(directory); err != nil || !reflect.DeepEqual(loaded, manifest) {
		t.Fatalf("valid release fixture prevalidation: manifest=%+v err=%v", loaded, err)
	}
	members := []stageMember{{header: tar.Header{Name: filepath.Base(directory), Typeflag: tar.TypeDir, Mode: 0o755}},
		{header: tar.Header{Name: "install-release.sh", Typeflag: tar.TypeReg, Mode: 0o755}, body: []byte("#!/bin/sh\nexit 0\n")}}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		path := filepath.Join(directory, entry.Name())
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		members = append(members, stageMember{header: tar.Header{Name: filepath.Base(directory) + "/" + entry.Name(), Typeflag: tar.TypeReg, Mode: int64(info.Mode().Perm())}, body: body})
	}
	return members, manifest
}

func stageArchive(t *testing.T, members []stageMember) []byte {
	t.Helper()
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	for _, member := range members {
		header := member.header
		header.Size = int64(len(member.body))
		if err := writer.WriteHeader(&header); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(member.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}

func TestFetchAndStageReleaseContract(t *testing.T) {
	members, expected := validStageMembers(t)
	core := -1
	for index, member := range members {
		if member.header.Name == "mdd-stage-test/core" {
			core = index
		}
	}
	if core == -1 {
		t.Fatal("fixture lacks core artifact")
	}
	type stageCase struct {
		name      string
		mutate    func([]stageMember) []stageMember
		asset     func(*releaseAsset)
		wantError string
		wantExist bool
	}
	cases := []stageCase{
		{name: "valid_permissions"},
		{name: "manifest_mode_mismatch", mutate: func(items []stageMember) []stageMember { items[core].header.Mode = 0o644; return items }, wantError: "release archive contains no valid release directory"},
		{name: "artifact_digest_mismatch", mutate: func(items []stageMember) []stageMember {
			items[core].body = bytes.Repeat([]byte("x"), len(items[core].body))
			return items
		}, wantError: "release archive contains no valid release directory"},
		{name: "download_digest_mismatch", asset: func(asset *releaseAsset) { asset.Digest = "sha256:" + strings.Repeat("0", 64) }, wantError: "release asset digest mismatch"},
		{name: "oversized_asset", asset: func(asset *releaseAsset) { asset.Size = maximumReleaseArchiveBytes + 1 }, wantError: "verified Linux release asset is unavailable"},
		{name: "traversal", mutate: func(items []stageMember) []stageMember { items[core].header.Name = "../escape"; return items }, wantError: "release archive member escapes destination"},
		{name: "absolute_path", mutate: func(items []stageMember) []stageMember { items[core].header.Name = "/escape"; return items }, wantError: "release archive member path is invalid"},
		{name: "duplicate", mutate: func(items []stageMember) []stageMember { return append(items, items[core]) }, wantExist: true},
		{name: "symlink", mutate: func(items []stageMember) []stageMember {
			items[core].header.Typeflag, items[core].header.Linkname, items[core].body = tar.TypeSymlink, "../escape", nil
			return items
		}, wantError: "release archive contains unsupported member type"},
	}
	for _, mode := range []int64{0o600, 0o666, 0o777, 0o4755, 0o2755, 0o1755, 0o100755} {
		cases = append(cases, stageCase{name: fmt.Sprintf("reject_mode_%o", mode), mutate: func(items []stageMember) []stageMember { items[core].header.Mode = mode; return items }, wantError: "release archive member mode is unsupported"})
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			items := append([]stageMember(nil), members...)
			if test.mutate != nil {
				items = test.mutate(items)
			}
			destination := filepath.Join(t.TempDir(), "stage")
			archive := stageArchive(t, items) // Each internal mutation gets its actual outer digest.
			staged, err := FetchAndStage(context.Background(), "example/project", "2.0.0", destination, &http.Client{Transport: updateTransport{archive: archive, asset: test.asset}})
			if test.wantError != "" || test.wantExist {
				if err == nil || staged != "" || (test.wantExist && !errors.Is(err, os.ErrExist)) || (test.wantError != "" && err.Error() != test.wantError) {
					t.Fatalf("wrong rejection: staged=%q err=%v want=%q exists=%t", staged, err, test.wantError, test.wantExist)
				}
				entries, readErr := os.ReadDir(destination)
				if len(entries) != 0 || (readErr != nil && !errors.Is(readErr, os.ErrNotExist)) {
					t.Fatalf("failed staging left files: entries=%v err=%v", entries, readErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("valid release staging failed: %v", err)
			}
			loaded, err := releasebundle.LoadDirectory(staged)
			if err != nil || !reflect.DeepEqual(loaded, expected) {
				t.Fatalf("staged content/identity/modes failed strict validation: manifest=%+v err=%v", loaded, err)
			}
			for _, path := range []string{destination, filepath.Dir(filepath.Dir(staged)), filepath.Dir(staged), staged} {
				info, err := os.Stat(path)
				if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
					t.Fatalf("staging directory is not private: %s info=%v err=%v", path, info, err)
				}
			}
			t.Log("valid release staged; complete manifest identity, hashes, sizes and both 0644/0755 modes verified")
		})
	}
}
