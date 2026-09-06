package agenthealth

import (
	"errors"
	"testing"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/buildidentity"
)

func TestCollectorProjectsBoundedHostAndQuantizedStorage(t *testing.T) {
	collector, err := New(Config{
		StoragePath: "/state", HostMode: "service", ModemEnabled: true, TokenConfigured: true,
		Platform: "linux", Architecture: "arm64", Identity: buildidentity.Identity{VCSRevision: "revision-1"},
		diskUsage: func(string) (uint64, uint64, error) { return 100 << 30, 10<<30 + 12345, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	fact := collector.Snapshot()
	if fact.Platform != "linux" || fact.Architecture != "arm64" || fact.BuildVersion != "revision-1" ||
		fact.Manager != "systemd" || fact.SessionScope != "machine" || fact.Storage.State != "warning" ||
		fact.Storage.UsedPercent != 90 || fact.Storage.FreeBytes != 10<<30 || fact.Validate() != nil {
		t.Fatalf("fact=%+v", fact)
	}
}

func TestCollectorKeepsStorageFailureUnknownWithoutInventingCapacity(t *testing.T) {
	collector, err := New(Config{
		StoragePath: "/state", HostMode: "gui", TokenConfigured: true,
		Platform: "macos", Architecture: "arm64", Identity: buildidentity.Identity{VCSRevision: "revision-1"},
		diskUsage: func(string) (uint64, uint64, error) { return 0, 0, errors.New("unavailable") },
	})
	if err != nil {
		t.Fatal(err)
	}
	fact := collector.Snapshot()
	if fact.Manager != "gui" || fact.SessionScope != "user" || fact.Storage.State != "unknown" ||
		fact.Storage.TotalBytes != 0 || fact.Storage.FreeBytes != 0 || fact.Storage.ErrorCode != "storage_unavailable" {
		t.Fatalf("fact=%+v", fact)
	}
}

func TestCollectorStorageThresholdsAndWindowsSCMMode(t *testing.T) {
	for used, want := range map[uint64]string{84: "ok", 85: "warning", 94: "warning", 95: "critical"} {
		t.Run(want, func(t *testing.T) {
			collector, err := New(Config{StoragePath: "/state", HostMode: "service",
				TokenConfigured: true, Platform: "windows", Architecture: "amd64",
				Identity:  buildidentity.Identity{VCSRevision: "revision-1"},
				diskUsage: func(string) (uint64, uint64, error) { return 100 << 30, (100 - used) << 30, nil }})
			if err != nil {
				t.Fatal(err)
			}
			fact := collector.Snapshot()
			if fact.Manager != "scm" || fact.SessionScope != "machine" || fact.Storage.State != want ||
				fact.Storage.UsedPercent != uint32(used) {
				t.Fatalf("fact=%+v", fact)
			}
		})
	}
}

func TestCollectorRejectsRelativeStorageOrUnsupportedMode(t *testing.T) {
	base := Config{StoragePath: "relative", HostMode: "gui", TokenConfigured: true,
		Platform: "macos", Architecture: "arm64", Identity: buildidentity.Identity{VCSRevision: "revision-1"},
		diskUsage: func(string) (uint64, uint64, error) { return 1, 1, nil }}
	if _, err := New(base); err == nil {
		t.Fatal("relative storage path was accepted")
	}
	base.StoragePath, base.HostMode = "/state", "daemon"
	if _, err := New(base); err == nil {
		t.Fatal("unsupported host mode was accepted")
	}
}

func TestCollectorReadsCurrentPlatformFilesystem(t *testing.T) {
	collector, err := New(Config{StoragePath: t.TempDir(), HostMode: "cli", TokenConfigured: true,
		Identity: buildidentity.Identity{VCSRevision: "revision-native"}})
	if err != nil {
		t.Fatal(err)
	}
	fact := collector.Snapshot()
	if fact.Storage.State == "unknown" || fact.Storage.TotalBytes == 0 || fact.Storage.FreeBytes > fact.Storage.TotalBytes ||
		fact.Validate() != nil {
		t.Fatalf("fact=%+v", fact)
	}
}
