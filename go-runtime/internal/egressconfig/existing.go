package egressconfig

import (
	"errors"
	"io"
	"os"
	"path/filepath"
)

// ReadExistingConfig is shared by explicit Apply and the unprivileged renderer.
// Neither caller includes the source path or file contents in public errors.
func ReadExistingConfig(path string) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("existing outbound requires an absolute configuration path")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 8<<20 {
		return nil, errors.New("existing outbound configuration is not a regular file within the size limit")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("existing outbound configuration cannot be opened")
	}
	defer file.Close()
	payload, err := io.ReadAll(io.LimitReader(file, (8<<20)+1))
	if err != nil || len(payload) > 8<<20 {
		return nil, errors.New("existing outbound configuration cannot be read within the size limit")
	}
	return payload, nil
}
