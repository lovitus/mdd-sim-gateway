package cellulario

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var deviceLine = regexp.MustCompile(`(?i)^device vid=([0-9a-f]{4}) pid=([0-9a-f]{4}) bus=([0-9]+) address=([0-9]+) serial=(.*)$`)

type Attachment struct {
	VID     uint16
	PID     uint16
	Bus     uint8
	Address uint8
	Serial  string
}

func (attachment Attachment) PhysicalID() string {
	stable := strings.TrimSpace(attachment.Serial)
	if stable == "" {
		stable = fmt.Sprintf("location:%d:%d", attachment.Bus, attachment.Address)
	}
	return fmt.Sprintf("usb:%04x:%04x:%s", attachment.VID, attachment.PID, stable)
}

func (attachment Attachment) Generation() string {
	return fmt.Sprintf("%s@%d:%d", attachment.PhysicalID(), attachment.Bus, attachment.Address)
}

func (attachment Attachment) ID() string {
	digest := sha256.Sum256([]byte(attachment.Generation()))
	return fmt.Sprintf("usb-%04x-%04x-%s", attachment.VID, attachment.PID, hex.EncodeToString(digest[:12]))
}

func ResolveSibling(name string) (string, error) {
	if name == "" || filepath.Base(name) != name {
		return "", errors.New("invalid companion executable name")
	}
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	candidate := filepath.Join(filepath.Dir(executable), name)
	info, err := os.Stat(candidate)
	if err != nil || info.IsDir() || info.Mode()&0111 == 0 {
		return "", fmt.Errorf("bundled companion %s is unavailable", name)
	}
	return candidate, nil
}

func Discover(ctx context.Context, executable string, exclude []Attachment) ([]Attachment, error) {
	if strings.TrimSpace(executable) == "" {
		return nil, errors.New("cellular companion path is empty")
	}
	arguments := []string{"--list"}
	sort.Slice(exclude, func(left, right int) bool {
		if exclude[left].Bus == exclude[right].Bus {
			return exclude[left].Address < exclude[right].Address
		}
		return exclude[left].Bus < exclude[right].Bus
	})
	for _, attachment := range exclude {
		arguments = append(arguments, "--exclude", fmt.Sprintf("%d:%d", attachment.Bus, attachment.Address))
	}
	command := exec.CommandContext(ctx, executable, arguments...)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		detail := boundedText(stderr.String(), 500)
		if detail == "" {
			detail = err.Error()
		}
		return nil, fmt.Errorf("enumerate raw USB modems: %s", detail)
	}
	result := make([]Attachment, 0)
	scanner := bufio.NewScanner(&stdout)
	for scanner.Scan() {
		match := deviceLine.FindStringSubmatch(strings.TrimSpace(scanner.Text()))
		if len(match) != 6 {
			continue
		}
		vid, vidErr := strconv.ParseUint(match[1], 16, 16)
		pid, pidErr := strconv.ParseUint(match[2], 16, 16)
		bus, busErr := strconv.ParseUint(match[3], 10, 8)
		address, addressErr := strconv.ParseUint(match[4], 10, 8)
		if errors.Join(vidErr, pidErr, busErr, addressErr) != nil || vid == 0 || pid == 0 {
			return nil, errors.New("cellular companion returned an invalid attachment")
		}
		result = append(result, Attachment{
			VID: uint16(vid), PID: uint16(pid), Bus: uint8(bus), Address: uint8(address),
			Serial: strings.TrimSpace(match[5]),
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Generation() < result[right].Generation() })
	for index := 1; index < len(result); index++ {
		if result[index-1].Generation() == result[index].Generation() {
			return nil, errors.New("cellular companion returned duplicate attachment generations")
		}
	}
	return result, nil
}

func boundedText(value string, maximum int) string {
	value = strings.TrimSpace(strings.ToValidUTF8(value, "?"))
	if len(value) > maximum {
		value = strings.ToValidUTF8(value[:maximum], "?")
	}
	return value
}
