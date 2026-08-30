//go:build windows

package windowspcm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/windowspnp"
)

const minimumStreamHelperVersion = 4

var comPortPattern = regexp.MustCompile(`(?i)^COM[0-9]+$`)

type UAC struct {
	helperPath string
	playback   string
	capture    string
}

type inventory struct {
	Endpoints []struct {
		Kind       string `json:"kind"`
		InstanceID string `json:"instance_id"`
		Status     string `json:"status"`
	} `json:"endpoints"`
}

type helperResult struct {
	OK               bool           `json:"ok"`
	Version          int            `json:"version"`
	Devices          []helperDevice `json:"devices"`
	SampleRate       uint32         `json:"sample_rate"`
	CaptureChannels  uint32         `json:"capture_channels"`
	PlaybackChannels uint32         `json:"playback_channels"`
	Error            string         `json:"error"`
}

func DiscoverUAC(ctx context.Context, physicalID string) (UAC, error) {
	ports, err := windowspnp.Ports()
	if err != nil {
		return UAC{}, err
	}
	serial, err := Select(ports, physicalID)
	if err != nil {
		return UAC{}, err
	}
	inv, err := audioInventory(ctx, serial.Name)
	if err != nil {
		return UAC{}, fmt.Errorf("inventory modem UAC endpoints: %w", err)
	}
	if len(inv.Endpoints) == 0 {
		return UAC{}, ErrUACUnavailable
	}
	helper, err := siblingHelper()
	if err != nil {
		return UAC{}, ErrUACUnavailable
	}
	listContext, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	output, err := exec.CommandContext(listContext, helper, "-mode", "list").Output()
	if err != nil {
		return UAC{}, fmt.Errorf("enumerate WASAPI endpoints: %w", err)
	}
	var listed helperResult
	if err := json.Unmarshal(output, &listed); err != nil || !listed.OK || listed.Version < minimumStreamHelperVersion {
		return UAC{}, errors.New("the bundled call-audio helper is invalid or too old")
	}
	endpoints := make([]uacEndpoint, 0, len(inv.Endpoints))
	for _, value := range inv.Endpoints {
		endpoints = append(endpoints, uacEndpoint{Kind: value.Kind, InstanceID: value.InstanceID, Status: value.Status})
	}
	playback, capture, err := selectUAC(endpoints, listed.Devices)
	if err != nil {
		return UAC{}, err
	}
	return UAC{helperPath: helper, playback: playback, capture: capture}, nil
}

func siblingHelper() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	path := filepath.Join(filepath.Dir(executable), "mdd-call-audio-helper.exe")
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return "", errors.New("the bundled call-audio helper is unavailable")
	}
	return path, nil
}

func audioInventory(ctx context.Context, port string) (inventory, error) {
	port = strings.ToUpper(strings.TrimSpace(port))
	if !comPortPattern.MatchString(port) {
		return inventory{}, errors.New("invalid modem COM port")
	}
	script := fmt.Sprintf(`
$ErrorActionPreference = 'Stop'
$portName = '%s'
$serial = Get-PnpDevice -Class Ports -PresentOnly | Where-Object {
    $_.FriendlyName -match ('\(' + [regex]::Escape($portName) + '\)$')
} | Select-Object -First 1
if ($null -eq $serial) { throw ('PnP serial port not found: ' + $portName) }
$container = (Get-PnpDeviceProperty -InstanceId $serial.InstanceId -KeyName 'DEVPKEY_Device_ContainerId').Data
$endpoints = @()
Get-PnpDevice -Class AudioEndpoint -PresentOnly | ForEach-Object {
    try {
        $candidate = (Get-PnpDeviceProperty -InstanceId $_.InstanceId -KeyName 'DEVPKEY_Device_ContainerId').Data
        if ($candidate -eq $container) {
            $kind = if ($_.InstanceId -match '\{0\.0\.0\.') { 'playback' } elseif ($_.InstanceId -match '\{0\.0\.1\.') { 'capture' } else { 'unknown' }
            $endpoints += [pscustomobject]@{ kind = $kind; instance_id = $_.InstanceId; status = [string]$_.Status }
        }
    } catch {}
}
[pscustomobject]@{ endpoints = @($endpoints) } | ConvertTo-Json -Depth 4 -Compress
`, port)
	words := utf16.Encode([]rune(script))
	encoded := make([]byte, len(words)*2)
	for index, word := range words {
		binary.LittleEndian.PutUint16(encoded[index*2:], word)
	}
	commandContext, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	output, err := exec.CommandContext(commandContext, "powershell.exe", "-NoProfile", "-NonInteractive",
		"-EncodedCommand", base64.StdEncoding.EncodeToString(encoded)).CombinedOutput()
	if err != nil {
		return inventory{}, fmt.Errorf("PowerShell inventory failed: %s", strings.TrimSpace(string(output)))
	}
	var value inventory
	if err := json.Unmarshal(output, &value); err != nil {
		return inventory{}, errors.New("Windows audio inventory returned invalid JSON")
	}
	return value, nil
}

func (config UAC) Open() (io.ReadWriteCloser, error) {
	command := exec.Command(config.helperPath, "-mode", "stream",
		"-playback-id", config.playback, "-capture-id", config.capture)
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
	readyCh := make(chan []byte, 1)
	go func() {
		line, _ := reader.ReadBytes('\n')
		readyCh <- line
	}()
	var line []byte
	select {
	case line = <-readyCh:
	case <-time.After(12 * time.Second):
		_ = command.Process.Kill()
		_ = command.Wait()
		return nil, errors.New("call-audio helper did not become ready")
	}
	var ready helperResult
	if err := json.Unmarshal(line, &ready); err != nil || !ready.OK || ready.Version < minimumStreamHelperVersion ||
		ready.SampleRate != 8000 || ready.CaptureChannels != 1 || ready.PlaybackChannels != 1 {
		_ = stdin.Close()
		_ = command.Process.Kill()
		_ = command.Wait()
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = ready.Error
		}
		return nil, fmt.Errorf("call-audio helper startup failed: %s", detail)
	}
	return &helperEndpoint{reader: reader, stdin: stdin, command: command}, nil
}

type helperEndpoint struct {
	reader  io.Reader
	stdin   io.WriteCloser
	command *exec.Cmd
	once    sync.Once
	err     error
}

func (endpoint *helperEndpoint) Read(value []byte) (int, error)  { return endpoint.reader.Read(value) }
func (endpoint *helperEndpoint) Write(value []byte) (int, error) { return endpoint.stdin.Write(value) }

func (endpoint *helperEndpoint) Close() error {
	endpoint.once.Do(func() {
		closeErr := endpoint.stdin.Close()
		done := make(chan error, 1)
		go func() { done <- endpoint.command.Wait() }()
		select {
		case waitErr := <-done:
			endpoint.err = errors.Join(closeErr, waitErr)
		case <-time.After(3 * time.Second):
			killErr := endpoint.command.Process.Kill()
			waitErr := <-done
			endpoint.err = errors.Join(closeErr, killErr, waitErr)
		}
	})
	return endpoint.err
}
