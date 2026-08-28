// Package hostpreflight rejects host filesystem combinations with known
// durability defects before a bbolt file is opened.
package hostpreflight

import (
	"encoding/binary"
	"errors"
	"strconv"
	"strings"
)

const (
	ext4SuperblockSize   = 1024
	ext4MagicOffset      = 0x38
	ext4CompatOffset     = 0x5c
	ext4Magic            = 0xef53
	ext4CompatFastCommit = 0x0400
)

type kernelVersion struct {
	major int
	minor int
	patch int
}

func parseKernelVersion(release string) (kernelVersion, error) {
	var version kernelVersion
	parts := strings.SplitN(strings.TrimSpace(release), ".", 3)
	if len(parts) < 2 {
		return version, errors.New("Linux kernel release is invalid")
	}
	values := []*int{&version.major, &version.minor, &version.patch}
	for index, part := range parts {
		if part == "" || part[0] < '0' || part[0] > '9' {
			return kernelVersion{}, errors.New("Linux kernel release is invalid")
		}
		digits := part
		end := strings.IndexFunc(digits, func(character rune) bool { return character < '0' || character > '9' })
		if end >= 0 {
			digits = digits[:end]
		}
		value, err := strconv.Atoi(digits)
		if err != nil {
			return kernelVersion{}, errors.New("Linux kernel release is invalid")
		}
		*values[index] = value
	}
	return version, nil
}

// needsFastCommitFeatureCheck reports kernels in the upstream bbolt warning
// window. Kernels before 5.10 do not implement ext4 fast commits. The fixes
// are present in 5.10.94+, 5.15.27+, and mainline 5.17+.
func needsFastCommitFeatureCheck(version kernelVersion) bool {
	if version.major != 5 {
		return false
	}
	switch {
	case version.minor < 10:
		return false
	case version.minor == 10:
		return version.patch < 94
	case version.minor < 15:
		return true
	case version.minor == 15:
		return version.patch < 27
	case version.minor == 16:
		return true
	default:
		return false
	}
}

func ext4FastCommitEnabled(superblock []byte) (bool, error) {
	if len(superblock) != ext4SuperblockSize || binary.LittleEndian.Uint16(superblock[ext4MagicOffset:]) != ext4Magic {
		return false, errors.New("ext4 superblock is invalid")
	}
	features := binary.LittleEndian.Uint32(superblock[ext4CompatOffset:])
	return features&ext4CompatFastCommit != 0, nil
}
