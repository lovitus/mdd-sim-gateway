// Package buildidentity exposes bounded, non-secret Go module and VCS build
// settings. It does not claim that a binary belongs to a verified MDD release;
// strict release provenance is owned by the release manifest verifier.
package buildidentity

import (
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
)

type Identity struct {
	GoVersion     string     `json:"go_version"`
	ModuleVersion string     `json:"module_version,omitempty"`
	VCSRevision   string     `json:"vcs_revision,omitempty"`
	VCSTime       *time.Time `json:"vcs_time,omitempty"`
	VCSModified   *bool      `json:"vcs_modified,omitempty"`
}

func Read() Identity {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return Identity{GoVersion: runtime.Version()}
	}
	return FromBuildInfo(info)
}

func FromBuildInfo(info *debug.BuildInfo) Identity {
	result := Identity{GoVersion: runtime.Version()}
	if info == nil {
		return result
	}
	if strings.TrimSpace(info.GoVersion) != "" {
		result.GoVersion = strings.TrimSpace(info.GoVersion)
	}
	if version := strings.TrimSpace(info.Main.Version); version != "" && version != "(devel)" && safe(version, 128) {
		result.ModuleVersion = version
	}
	settings := make(map[string]string, len(info.Settings))
	for _, setting := range info.Settings {
		settings[setting.Key] = strings.TrimSpace(setting.Value)
	}
	if revision := settings["vcs.revision"]; safe(revision, 128) {
		result.VCSRevision = revision
	}
	if parsed, err := time.Parse(time.RFC3339, settings["vcs.time"]); err == nil {
		parsed = parsed.UTC()
		result.VCSTime = &parsed
	}
	if value, err := strconv.ParseBool(settings["vcs.modified"]); err == nil {
		result.VCSModified = &value
	}
	return result
}

func (identity Identity) DisplayVersion() string {
	if identity.ModuleVersion != "" {
		return identity.ModuleVersion
	}
	if identity.VCSRevision != "" {
		return identity.VCSRevision
	}
	return "unavailable"
}

func safe(value string, maximum int) bool {
	if value == "" || len(value) > maximum {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}
