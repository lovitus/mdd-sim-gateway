//go:build darwin

package darwinmodem

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/cellulario"
)

const minimumAudioHelperVersion = 4

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

type modemUAC struct {
	helper   string
	playback string
	capture  string
}

func (prober *Prober) OpenVoicePCM(ctx context.Context, target agentmodem.MediaTarget) (io.ReadWriteCloser, error) {
	prober.mu.Lock()
	defer prober.mu.Unlock()
	facts, err := prober.probeLocked(ctx, true, false)
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
	if privateDataOwnsSIM(current) {
		return nil, fmt.Errorf("%w: private cellular data owns SIM operations", agentmodem.ErrOperationUnavailable)
	}
	uac, err := discoverModemUAC(ctx, current.attachment, prober.audioHelper)
	if err != nil {
		return nil, err
	}
	if err := current.owner.EnableVoicePCMMode(ctx, 2); err != nil {
		return nil, fmt.Errorf("enable modem UAC voice route: %w", err)
	}
	endpoint, err := uac.Open()
	if err != nil {
		_ = current.owner.DisableVoicePCM(ctx)
		return nil, err
	}
	return &voiceEndpoint{
		ReadWriteCloser: endpoint, prober: prober,
		attachmentID: target.AttachmentID, equipmentID: target.EquipmentID,
	}, nil
}

type voiceEndpoint struct {
	io.ReadWriteCloser
	prober       *Prober
	attachmentID string
	equipmentID  string
	once         sync.Once
	err          error
}

func (*voiceEndpoint) PCMWriteBatchBytes() int { return 320 }

func (endpoint *voiceEndpoint) Close() error {
	endpoint.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		endpoint.prober.mu.Lock()
		current := endpoint.prober.find(endpoint.attachmentID, endpoint.equipmentID)
		var disableErr error
		if current != nil {
			disableErr = current.owner.DisableVoicePCM(ctx)
		}
		endpoint.prober.mu.Unlock()
		endpoint.err = errors.Join(disableErr, endpoint.ReadWriteCloser.Close())
	})
	return endpoint.err
}

func discoverModemUAC(ctx context.Context, attachment cellulario.Attachment, helper string) (modemUAC, error) {
	serial := strings.TrimSpace(attachment.Serial)
	if serial == "" {
		return modemUAC{}, errors.New("raw USB modem has no serial for exact CoreAudio ownership")
	}
	ioreg := exec.CommandContext(ctx, "/usr/sbin/ioreg", "-r", "-c", "AppleUSBAudioEngine", "-a", "-l")
	registry, err := ioreg.Output()
	if err != nil {
		return modemUAC{}, fmt.Errorf("inventory macOS USB audio engines: %w", err)
	}
	plutil := exec.CommandContext(ctx, "/usr/bin/plutil", "-convert", "json", "-o", "-", "-")
	plutil.Stdin = bytes.NewReader(registry)
	converted, err := plutil.Output()
	if err != nil {
		return modemUAC{}, fmt.Errorf("decode macOS USB audio inventory: %w", err)
	}
	var engines []map[string]any
	if err := json.Unmarshal(converted, &engines); err != nil {
		return modemUAC{}, errors.New("macOS USB audio inventory returned invalid JSON")
	}
	wanted := make(map[string]string)
	for _, engine := range engines {
		if number(engine["idVendor"]) != uint64(attachment.VID) || number(engine["idProduct"]) != uint64(attachment.PID) {
			continue
		}
		uid, _ := engine["IOAudioEngineGlobalUniqueID"].(string)
		if uid == "" || !containsToken(uid, serial) {
			continue
		}
		directions := map[uint64]bool{}
		children, _ := engine["IORegistryEntryChildren"].([]any)
		for _, raw := range children {
			child, _ := raw.(map[string]any)
			if child["IOObjectClass"] == "AppleUSBAudioStream" {
				directions[number(child["IOAudioStreamDirection"])] = true
			}
		}
		kind := ""
		if directions[1] {
			kind = "capture"
		} else if directions[0] {
			kind = "playback"
		}
		if kind == "" {
			continue
		}
		if wanted[kind] != "" {
			return modemUAC{}, errors.New("raw USB modem exposes ambiguous CoreAudio endpoints")
		}
		wanted[kind] = uid
	}
	if wanted["playback"] == "" || wanted["capture"] == "" {
		return modemUAC{}, errors.New("raw USB modem has no exact full-duplex CoreAudio endpoints")
	}
	listed, err := runAudioHelper(ctx, helper, "-mode", "list")
	if err != nil {
		return modemUAC{}, err
	}
	selected := make(map[string]string)
	for _, item := range listed.Devices {
		decoded, decodeErr := hex.DecodeString(strings.TrimSpace(item.ID))
		if decodeErr != nil {
			continue
		}
		uid := strings.TrimRight(string(decoded), "\x00")
		if wanted[item.Kind] == uid {
			if selected[item.Kind] != "" {
				return modemUAC{}, errors.New("call-audio helper returned duplicate exact endpoints")
			}
			selected[item.Kind] = item.ID
		}
	}
	if selected["playback"] == "" || selected["capture"] == "" {
		return modemUAC{}, errors.New("call-audio helper did not expose the modem's exact CoreAudio pair")
	}
	return modemUAC{helper: helper, playback: selected["playback"], capture: selected["capture"]}, nil
}

func runAudioHelper(ctx context.Context, helper string, arguments ...string) (audioHelperResult, error) {
	command := exec.CommandContext(ctx, helper, arguments...)
	output, err := command.CombinedOutput()
	var result audioHelperResult
	decodeErr := json.Unmarshal(output, &result)
	if err != nil || decodeErr != nil || !result.OK || result.Version < minimumAudioHelperVersion {
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

func (uac modemUAC) Open() (io.ReadWriteCloser, error) {
	command := exec.Command(uac.helper, "-mode", "stream", "-playback-id", uac.playback, "-capture-id", uac.capture)
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
		result.SampleRate != 8000 || result.CaptureChannels != 1 || result.PlaybackChannels != 1 {
		_ = stdin.Close()
		_ = command.Process.Kill()
		_ = command.Wait()
		detail := bounded(stderr.String(), 500)
		if detail == "" {
			detail = bounded(result.Error, 500)
		}
		return nil, fmt.Errorf("call-audio helper startup failed: %s", detail)
	}
	return &audioEndpoint{reader: reader, stdin: stdin, command: command}, nil
}

type audioEndpoint struct {
	reader  io.Reader
	stdin   io.WriteCloser
	command *exec.Cmd
	once    sync.Once
	err     error
}

func (endpoint *audioEndpoint) Read(value []byte) (int, error)  { return endpoint.reader.Read(value) }
func (endpoint *audioEndpoint) Write(value []byte) (int, error) { return endpoint.stdin.Write(value) }

func (endpoint *audioEndpoint) Close() error {
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

func containsToken(value, token string) bool {
	for _, part := range strings.Split(value, ":") {
		if part == token {
			return true
		}
	}
	return false
}

func number(value any) uint64 {
	switch current := value.(type) {
	case float64:
		if current >= 0 {
			return uint64(current)
		}
	case json.Number:
		result, _ := strconv.ParseUint(current.String(), 0, 64)
		return result
	case string:
		result, _ := strconv.ParseUint(current, 0, 64)
		return result
	}
	return 0
}
