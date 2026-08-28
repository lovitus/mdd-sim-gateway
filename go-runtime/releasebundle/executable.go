package releasebundle

import (
	"debug/buildinfo"
	"errors"
	"os"
)

// InspectGoExecutable verifies that path is a Go executable for the declared
// target. It reads embedded build metadata and never executes the artifact.
func InspectGoExecutable(path, targetOS, targetArchitecture string) (string, error) {
	info, statErr := os.Lstat(path)
	if statErr != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("Go executable must be a regular file")
	}
	build, err := buildinfo.ReadFile(path)
	if err != nil {
		return "", errors.New("release executable has no valid Go build information")
	}
	settings := make(map[string]string, len(build.Settings))
	for _, setting := range build.Settings {
		settings[setting.Key] = setting.Value
	}
	if settings["GOOS"] != targetOS || settings["GOARCH"] != targetArchitecture || build.GoVersion == "" {
		return "", errors.New("release executable target does not match manifest")
	}
	return build.GoVersion, nil
}
