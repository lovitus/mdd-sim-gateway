package buildidentity

import (
	"runtime/debug"
	"testing"
	"time"
)

func TestFromBuildInfoSeparatesModuleAndVCSIdentity(t *testing.T) {
	info := &debug.BuildInfo{
		GoVersion: "go1.26.3",
		Main:      debug.Module{Version: "(devel)"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "bc378f003960645e89ac133f84fcf583a5dfb1f7"},
			{Key: "vcs.time", Value: "2026-09-01T02:17:39Z"},
			{Key: "vcs.modified", Value: "false"},
		},
	}
	identity := FromBuildInfo(info)
	if identity.ModuleVersion != "" || identity.VCSRevision != info.Settings[0].Value ||
		identity.VCSTime == nil || !identity.VCSTime.Equal(time.Date(2026, 9, 1, 2, 17, 39, 0, time.UTC)) ||
		identity.VCSModified == nil || *identity.VCSModified || identity.DisplayVersion() != identity.VCSRevision {
		t.Fatalf("identity=%+v", identity)
	}
}

func TestFromBuildInfoRejectsUnsafeOrInvalidSettings(t *testing.T) {
	info := &debug.BuildInfo{Main: debug.Module{Version: "release\nsecret"}, Settings: []debug.BuildSetting{
		{Key: "vcs.revision", Value: "bad\nrevision"},
		{Key: "vcs.time", Value: "not-a-time"},
		{Key: "vcs.modified", Value: "maybe"},
	}}
	identity := FromBuildInfo(info)
	if identity.ModuleVersion != "" || identity.VCSRevision != "" || identity.VCSTime != nil ||
		identity.VCSModified != nil || identity.DisplayVersion() != "unavailable" {
		t.Fatalf("identity=%+v", identity)
	}
}
