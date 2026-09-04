package systemupdate

import (
	"archive/tar"
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
	"strings"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/releasebundle"
)

const maximumReleaseArchiveBytes = 512 << 20

type releaseAPI struct {
	Tag    string         `json:"tag_name"`
	Assets []releaseAsset `json:"assets"`
}
type releaseAsset struct {
	Name   string `json:"name"`
	URL    string `json:"browser_download_url"`
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

// FetchAndStage downloads exactly one Linux release asset from the configured
// GitHub release and extracts it into a fresh private directory. It verifies
// the release asset's advertised SHA-256 digest when present and rejects
// traversal, symlinks and oversized tar members.
func FetchAndStage(ctx context.Context, repository, target, destination string, client *http.Client) (string, error) {
	repository, target, destination = strings.TrimSpace(repository), strings.TrimPrefix(strings.TrimSpace(target), "v"), filepath.Clean(strings.TrimSpace(destination))
	if !repositoryPattern.MatchString(repository) || target == "" || !filepath.IsAbs(destination) || destination == string(filepath.Separator) {
		return "", errors.New("invalid release staging input")
	}
	if client == nil {
		client = &http.Client{}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/"+repository+"/releases/tags/v"+target, nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "mdd-sim-gateway-updater")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("release lookup returned HTTP %d", response.StatusCode)
	}
	var release releaseAPI
	if err := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&release); err != nil {
		return "", err
	}
	assetName := "mdd-" + target + "-linux-amd64.tar"
	var asset *releaseAsset
	for index := range release.Assets {
		if release.Assets[index].Name == assetName {
			asset = &release.Assets[index]
			break
		}
	}
	if asset == nil || asset.URL == "" || asset.Size <= 0 || asset.Size > maximumReleaseArchiveBytes || !strings.HasPrefix(strings.TrimSpace(asset.Digest), "sha256:") {
		return "", errors.New("verified Linux release asset is unavailable")
	}
	assetRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.URL, nil)
	if err != nil {
		return "", err
	}
	assetRequest.Header.Set("Accept", "application/octet-stream")
	assetRequest.Header.Set("User-Agent", "mdd-sim-gateway-updater")
	assetResponse, err := client.Do(assetRequest)
	if err != nil {
		return "", err
	}
	defer assetResponse.Body.Close()
	if assetResponse.StatusCode != http.StatusOK {
		return "", fmt.Errorf("release download returned HTTP %d", assetResponse.StatusCode)
	}
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return "", err
	}
	temporary, err := os.MkdirTemp(destination, ".release-stage-")
	if err != nil {
		return "", err
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.RemoveAll(temporary)
		}
	}()
	archivePath := filepath.Join(temporary, assetName)
	output, err := os.OpenFile(archivePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	digest := sha256.New()
	written, copyErr := io.CopyN(io.MultiWriter(output, digest), assetResponse.Body, maximumReleaseArchiveBytes+1)
	closeErr := output.Close()
	if copyErr != nil && !errors.Is(copyErr, io.EOF) {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	if written <= 0 || written > maximumReleaseArchiveBytes {
		return "", errors.New("release archive exceeds maximum size")
	}
	if expected := strings.TrimPrefix(strings.TrimSpace(asset.Digest), "sha256:"); expected != "" {
		actual := hex.EncodeToString(digest.Sum(nil))
		if actual != expected {
			return "", errors.New("release asset digest mismatch")
		}
	}
	root := filepath.Join(temporary, "extracted")
	if err := os.Mkdir(root, 0o700); err != nil {
		return "", err
	}
	if err := extractTar(archivePath, root); err != nil {
		return "", err
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 2 {
		return "", errors.New("release archive layout is invalid")
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == "" {
			continue
		}
		candidate := filepath.Join(root, entry.Name())
		if _, err := releasebundle.LoadDirectory(candidate); err == nil {
			complete = true
			return candidate, nil
		}
	}
	return "", errors.New("release archive contains no valid release directory")
}

func extractTar(archivePath, root string) error {
	input, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer input.Close()
	reader := tar.NewReader(io.LimitReader(input, maximumReleaseArchiveBytes+1))
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if header.Name == "" || filepath.IsAbs(header.Name) || strings.ContainsAny(header.Name, "\\\x00") {
			return errors.New("release archive member path is invalid")
		}
		target := filepath.Join(root, filepath.Clean(header.Name))
		if filepath.Dir(target) != root && !strings.HasPrefix(target, root+string(filepath.Separator)) {
			return errors.New("release archive member escapes destination")
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
		case tar.TypeReg:
			if header.Size < 0 || header.Size > maximumReleaseArchiveBytes {
				return errors.New("release archive member is too large")
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err != nil {
				return err
			}
			_, copyErr := io.CopyN(output, reader, header.Size)
			closeErr := output.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		default:
			return errors.New("release archive contains unsupported member type")
		}
	}
}
