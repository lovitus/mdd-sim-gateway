// Package scopedtoken persists one narrow host-local bearer credential.
package scopedtoken

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/moby/sys/atomicwriter"
)

func Ensure(path string) (string, error) {
	if token, err := Read(path); err == nil {
		return token, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(buffer)
	if err := atomicwriter.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		return "", err
	}
	if runtime.GOOS != "windows" {
		directory, err := os.Open(filepath.Dir(path))
		if err != nil {
			return "", err
		}
		if err := errors.Join(directory.Sync(), directory.Close()); err != nil {
			return "", err
		}
	}
	return token, nil
}

func Read(path string) (string, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	if !filepath.IsAbs(path) || path == string(filepath.Separator) {
		return "", errors.New("invalid scoped token path")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() < 32 || info.Size() > 512 {
		return "", errors.New("scoped token file is invalid")
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(payload))
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(decoded) != 32 {
		return "", errors.New("scoped token is invalid")
	}
	return token, nil
}
