package hostpreflight

import (
	"encoding/binary"
	"testing"
)

func TestKernelFastCommitRiskWindow(t *testing.T) {
	tests := map[string]bool{
		"3.10.0-1160.el7": false,
		"5.10.93":         true,
		"5.10.94":         false,
		"5.14.21-custom":  true,
		"5.15.26":         true,
		"5.15.27":         false,
		"5.16.20":         true,
		"5.17.0":          false,
		"6.12.1":          false,
	}
	for release, want := range tests {
		version, err := parseKernelVersion(release)
		if err != nil || needsFastCommitFeatureCheck(version) != want {
			t.Fatalf("release=%s version=%+v risk=%v want=%v err=%v", release, version, needsFastCommitFeatureCheck(version), want, err)
		}
	}
}

func TestExt4FastCommitFeature(t *testing.T) {
	superblock := make([]byte, ext4SuperblockSize)
	binary.LittleEndian.PutUint16(superblock[ext4MagicOffset:], ext4Magic)
	if enabled, err := ext4FastCommitEnabled(superblock); err != nil || enabled {
		t.Fatalf("enabled=%v err=%v", enabled, err)
	}
	binary.LittleEndian.PutUint32(superblock[ext4CompatOffset:], ext4CompatFastCommit)
	if enabled, err := ext4FastCommitEnabled(superblock); err != nil || !enabled {
		t.Fatalf("enabled=%v err=%v", enabled, err)
	}
	superblock[ext4MagicOffset] = 0
	if _, err := ext4FastCommitEnabled(superblock); err == nil {
		t.Fatal("invalid ext4 superblock was accepted")
	}
}
