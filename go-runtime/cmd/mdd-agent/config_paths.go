package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type configCandidate struct {
	Path, AgentID string
	Err           error
}

func configLocations() ([]string, error) {
	root, err := os.UserConfigDir()
	var paths []string
	if err == nil {
		name := "MDD Agent"
		if runtime.GOOS == "linux" {
			name = "mdd-agent"
		}
		paths = append(paths, filepath.Join(root, name, "config.json"))
	}
	switch runtime.GOOS {
	case "linux":
		if os.Geteuid() == 0 {
			paths = append([]string{"/var/lib/mdd-agent/config.json", "/etc/mdd-agent/config.json"}, paths...)
		} else {
			paths = append(paths, "/var/lib/mdd-agent/config.json", "/etc/mdd-agent/config.json")
		}
	case "darwin":
		paths = append(paths, "/Library/Application Support/MDD Agent/config.json", "/etc/mdd-agent/config.json")
	case "windows":
		if root := os.Getenv("ProgramData"); filepath.IsAbs(root) {
			paths = append(paths, filepath.Join(root, "MDD", "GoAgent", "config.json"))
		}
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("resolve default configuration directories: %w", err)
	}
	return paths, nil
}

func explicitOrEnvironmentPath(path string) (string, error) {
	if path == "" {
		path = strings.TrimSpace(os.Getenv("MDD_AGENT_CONFIG"))
	}
	if path == "" {
		return "", nil
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) == string(filepath.Separator) {
		return "", errors.New("-config / MDD_AGENT_CONFIG must name an absolute file path")
	}
	return filepath.Clean(path), nil
}

func discoverConfigs(paths []string) []configCandidate {
	var found []configCandidate
	seen := map[string]bool{}
	for _, path := range paths {
		path = filepath.Clean(path)
		if seen[path] {
			continue
		}
		seen[path] = true
		settings, err := readConfigForEdit(path, false)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		found = append(found, configCandidate{Path: path, AgentID: settings.Agent.ID, Err: err})
	}
	return found
}

func resolveConfigPath(requested string, choose func([]configCandidate) (string, error)) (string, error) {
	path, err := explicitOrEnvironmentPath(requested)
	if err != nil || path != "" {
		return path, err
	}
	paths, err := configLocations()
	if err != nil {
		return "", err
	}
	found := discoverConfigs(paths)
	if len(found) == 0 {
		return paths[0], nil
	}
	if len(found) == 1 {
		return found[0].Path, found[0].Err
	}
	if choose != nil {
		return choose(found)
	}
	names := make([]string, 0, len(found))
	for _, candidate := range found {
		names = append(names, candidate.Path)
	}
	return "", fmt.Errorf("multiple Agent configurations; select one with -config: %s", strings.Join(names, ", "))
}
