//go:build linux

package linuxmodem

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
)

const minimumAudioHelperVersion = 4

var alsaHardwareID = regexp.MustCompile(`^:([0-9]+),([0-9]+)$`)

type audioHelperResult struct {
	OK               bool                `json:"ok"`
	Version          int                 `json:"version"`
	Backend          string              `json:"backend"`
	Devices          []audioHelperDevice `json:"devices"`
	SampleRate       uint32              `json:"sample_rate"`
	CaptureChannels  uint32              `json:"capture_channels"`
	PlaybackChannels uint32              `json:"playback_channels"`
	Error            string              `json:"error"`
}

type audioHelperDevice struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	ID   string `json:"id"`
}

type linuxUAC struct {
	helper   string
	playback string
	capture  string
}

func (prober *Prober) OpenVoicePCM(ctx context.Context, target agentmodem.MediaTarget) (io.ReadWriteCloser, error) {
	prober.mu.Lock()
	defer prober.mu.Unlock()
	facts, err := prober.probeLocked(ctx, true)
	if err != nil {
		return nil, err
	}
	if err := agentmodem.ValidateMediaTarget(facts, target); err != nil {
		return nil, err
	}
	current := prober.find(target.AttachmentID, target.EquipmentID)
	if current == nil {
		return nil, agentmodem.ErrOperationTargetReplaced
	}
	uac, err := discoverLinuxUAC(ctx, prober.sysRoot, current.usb.PhysicalID, prober.audioHelper)
	if err != nil {
		return nil, err
	}
	if err := prober.at.EnableVoicePCMMode(ctx, target.EquipmentID, 2); err != nil {
		return nil, fmt.Errorf("enable modem UAC voice route: %w", err)
	}
	endpoint, err := uac.Open()
	if err != nil {
		_ = prober.at.DisableVoicePCM(ctx, target.EquipmentID)
		return nil, err
	}
	return &linuxVoiceEndpoint{
		ReadWriteCloser: endpoint, prober: prober,
		attachmentID: target.AttachmentID, equipmentID: target.EquipmentID,
	}, nil
}

type linuxVoiceEndpoint struct {
	io.ReadWriteCloser
	prober       *Prober
	attachmentID string
	equipmentID  string
	once         sync.Once
	err          error
}

func (*linuxVoiceEndpoint) PCMWriteBatchBytes() int { return 320 }

func (endpoint *linuxVoiceEndpoint) Close() error {
	endpoint.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		endpoint.prober.mu.Lock()
		current := endpoint.prober.find(endpoint.attachmentID, endpoint.equipmentID)
		var disableErr error
		if current != nil {
			disableErr = endpoint.prober.at.DisableVoicePCM(ctx, endpoint.equipmentID)
		}
		endpoint.prober.mu.Unlock()
		endpoint.err = errors.Join(disableErr, endpoint.ReadWriteCloser.Close())
	})
	return endpoint.err
}

func (prober *Prober) find(attachmentID, equipmentID string) *ownedDevice {
	for _, current := range prober.devices {
		if current.usb.AttachmentID == attachmentID && current.snapshot.EquipmentID == equipmentID {
			return current
		}
	}
	return nil
}

func discoverLinuxUAC(ctx context.Context, sysRoot, physicalID, helper string) (linuxUAC, error) {
	cards, err := soundCardsForPhysical(sysRoot, physicalID)
	if err != nil {
		return linuxUAC{}, err
	}
	listed, err := runLinuxAudioHelper(ctx, helper, "-backend", "alsa", "-mode", "list")
	if err != nil {
		return linuxUAC{}, err
	}
	selected := map[string][]string{"playback": {}, "capture": {}}
	for _, device := range listed.Devices {
		card, _, ok := decodeALSAHardwareID(device.ID)
		if !ok {
			continue
		}
		if _, matches := cards[card]; matches && (device.Kind == "playback" || device.Kind == "capture") {
			selected[device.Kind] = append(selected[device.Kind], device.ID)
		}
	}
	for kind := range selected {
		sort.Strings(selected[kind])
		if len(selected[kind]) != 1 {
			return linuxUAC{}, fmt.Errorf("exact modem ALSA %s endpoint count is %d", kind, len(selected[kind]))
		}
	}
	return linuxUAC{helper: helper, playback: selected["playback"][0], capture: selected["capture"][0]}, nil
}

func soundCardsForPhysical(sysRoot, physicalID string) (map[int]struct{}, error) {
	entries, err := os.ReadDir(filepath.Join(sysRoot, "class", "sound"))
	if err != nil {
		return nil, fmt.Errorf("inventory Linux sound cards: %w", err)
	}
	result := make(map[int]struct{})
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "card") {
			continue
		}
		card, err := strconv.Atoi(strings.TrimPrefix(entry.Name(), "card"))
		if err != nil || card < 0 {
			continue
		}
		path, err := filepath.EvalSymlinks(filepath.Join(sysRoot, "class", "sound", entry.Name(), "device"))
		if err != nil {
			continue
		}
		if sameUSBTree(path, physicalID) {
			result[card] = struct{}{}
		}
	}
	if len(result) == 0 {
		return nil, errors.New("modem has no exact ALSA card under its USB parent")
	}
	return result, nil
}

func sameUSBTree(path, physical string) bool {
	path, physical = filepath.Clean(path), filepath.Clean(physical)
	return path == physical || strings.HasPrefix(path, physical+string(filepath.Separator)) ||
		strings.HasPrefix(path, physical+":")
}

func decodeALSAHardwareID(value string) (card, device int, ok bool) {
	payload, err := hex.DecodeString(strings.TrimSpace(value))
	if err != nil {
		return 0, 0, false
	}
	match := alsaHardwareID.FindStringSubmatch(strings.TrimRight(string(payload), "\x00"))
	if len(match) != 3 {
		return 0, 0, false
	}
	card, cardErr := strconv.Atoi(match[1])
	device, deviceErr := strconv.Atoi(match[2])
	return card, device, errors.Join(cardErr, deviceErr) == nil
}

func runLinuxAudioHelper(ctx context.Context, helper string, arguments ...string) (audioHelperResult, error) {
	command := exec.CommandContext(ctx, helper, arguments...)
	output, err := command.CombinedOutput()
	var result audioHelperResult
	decodeErr := json.Unmarshal(output, &result)
	if err != nil || decodeErr != nil || !result.OK || result.Version < minimumAudioHelperVersion || result.Backend != "alsa" {
		detail := bounded(result.Error, 500)
		if detail == "" {
			detail = bounded(string(output), 500)
		}
		if detail == "" && err != nil {
			detail = err.Error()
		}
		return audioHelperResult{}, fmt.Errorf("call-audio helper failed: %s", detail)
	}
	return result, nil
}

func (uac linuxUAC) Open() (io.ReadWriteCloser, error) {
	command := exec.Command(uac.helper, "-backend", "alsa", "-mode", "stream", "-playback-id", uac.playback, "-capture-id", uac.capture)
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		return nil, err
	}
	reader := bufio.NewReaderSize(stdout, 4096)
	ready := make(chan []byte, 1)
	go func() {
		line, _ := reader.ReadBytes('\n')
		ready <- line
	}()
	var line []byte
	select {
	case line = <-ready:
	case <-time.After(12 * time.Second):
		_ = command.Process.Kill()
		_ = command.Wait()
		return nil, errors.New("call-audio helper did not become ready")
	}
	var result audioHelperResult
	if err := json.Unmarshal(line, &result); err != nil || !result.OK || result.Version < minimumAudioHelperVersion ||
		result.Backend != "alsa" || result.SampleRate != 8000 || result.CaptureChannels != 1 || result.PlaybackChannels != 1 {
		_ = stdin.Close()
		_ = command.Process.Kill()
		_ = command.Wait()
		detail := bounded(stderr.String(), 500)
		if detail == "" {
			detail = bounded(result.Error, 500)
		}
		return nil, fmt.Errorf("call-audio helper startup failed: %s", detail)
	}
	return &linuxAudioEndpoint{reader: reader, stdin: stdin, command: command}, nil
}

type linuxAudioEndpoint struct {
	reader  io.Reader
	stdin   io.WriteCloser
	command *exec.Cmd
	once    sync.Once
	err     error
}

func (endpoint *linuxAudioEndpoint) Read(value []byte) (int, error) {
	return endpoint.reader.Read(value)
}
func (endpoint *linuxAudioEndpoint) Write(value []byte) (int, error) {
	return endpoint.stdin.Write(value)
}

func (endpoint *linuxAudioEndpoint) Close() error {
	endpoint.once.Do(func() {
		closeErr := endpoint.stdin.Close()
		done := make(chan error, 1)
		go func() { done <- endpoint.command.Wait() }()
		select {
		case waitErr := <-done:
			endpoint.err = errors.Join(closeErr, waitErr)
		case <-time.After(3 * time.Second):
			killErr := endpoint.command.Process.Kill()
			endpoint.err = errors.Join(closeErr, killErr, <-done)
		}
	})
	return endpoint.err
}
