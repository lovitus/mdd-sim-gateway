package main

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gen2brain/malgo"
)

// Version 4 locks the local raw-PCM stream to the modem's proven 40 ms UAC
// period. Versions 2 through 4 retain the legacy bridge telemetry contract.
const helperVersion = 4

type audioDevice struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	ID   string `json:"id"`
}

type result struct {
	OK               bool          `json:"ok"`
	Version          int           `json:"version"`
	Backend          string        `json:"backend,omitempty"`
	Devices          []audioDevice `json:"devices,omitempty"`
	CaptureFrames    uint64        `json:"capture_frames,omitempty"`
	CapturePeak      uint32        `json:"capture_peak,omitempty"`
	SampleRate       uint32        `json:"sample_rate,omitempty"`
	CaptureChannels  uint32        `json:"capture_channels,omitempty"`
	PlaybackChannels uint32        `json:"playback_channels,omitempty"`
	Error            string        `json:"error,omitempty"`
}

func emit(value result) {
	_ = json.NewEncoder(os.Stdout).Encode(value)
}

func backendForPlatform() ([]malgo.Backend, string) {
	switch {
	case isWindows:
		return []malgo.Backend{malgo.BackendWasapi}, "wasapi"
	case isDarwin:
		return []malgo.Backend{malgo.BackendCoreaudio}, "coreaudio"
	default:
		// PulseAudio is preferred when available; miniaudio falls back to ALSA. The helper
		// reports unavailable rather than selecting the null backend on headless systems.
		return []malgo.Backend{malgo.BackendPulseaudio, malgo.BackendAlsa}, "pulseaudio/alsa"
	}
}

func enumerate(ctx *malgo.AllocatedContext) ([]audioDevice, error) {
	var devices []audioDevice
	for _, kind := range []struct {
		typeID malgo.DeviceType
		name   string
	}{{malgo.Playback, "playback"}, {malgo.Capture, "capture"}} {
		values, err := ctx.Devices(kind.typeID)
		if err != nil {
			return nil, err
		}
		for _, value := range values {
			devices = append(devices, audioDevice{
				Kind: kind.name, Name: value.Name(), ID: value.ID.String(),
			})
		}
	}
	return devices, nil
}

func selectDevice(values []malgo.DeviceInfo, id string) (*malgo.DeviceInfo, error) {
	for index := range values {
		if strings.EqualFold(values[index].ID.String(), id) {
			value := values[index]
			return &value, nil
		}
	}
	return nil, errors.New("the requested audio endpoint is not present")
}

func probe(ctx *malgo.AllocatedContext, playbackID, captureID string,
	duration time.Duration) (result, error) {
	playbacks, err := ctx.Devices(malgo.Playback)
	if err != nil {
		return result{}, err
	}
	captures, err := ctx.Devices(malgo.Capture)
	if err != nil {
		return result{}, err
	}
	playback, err := selectDevice(playbacks, playbackID)
	if err != nil {
		return result{}, fmt.Errorf("playback endpoint: %w", err)
	}
	capture, err := selectDevice(captures, captureID)
	if err != nil {
		return result{}, fmt.Errorf("capture endpoint: %w", err)
	}

	config := malgo.DefaultDeviceConfig(malgo.Duplex)
	config.SampleRate = 8000
	config.PeriodSizeInFrames = 320
	config.Playback.DeviceID = playback.ID.Pointer()
	config.Playback.Format = malgo.FormatS16
	config.Playback.Channels = 1
	config.Capture.DeviceID = capture.ID.Pointer()
	config.Capture.Format = malgo.FormatS16
	config.Capture.Channels = 1

	var frames atomic.Uint64
	var peak atomic.Uint32
	device, err := malgo.InitDevice(ctx.Context, config, malgo.DeviceCallbacks{
		Data: func(output, input []byte, frameCount uint32) {
			clear(output)
			frames.Add(uint64(frameCount))
			var local uint32
			for offset := 0; offset+1 < len(input); offset += 2 {
				value := int32(int16(binary.LittleEndian.Uint16(input[offset:])))
				if value < 0 {
					value = -value
				}
				if uint32(value) > local {
					local = uint32(value)
				}
			}
			for previous := peak.Load(); local > previous && !peak.CompareAndSwap(previous, local); previous = peak.Load() {
			}
		},
	})
	if err != nil {
		return result{}, err
	}
	defer device.Uninit()
	if err := device.Start(); err != nil {
		return result{}, err
	}
	time.Sleep(duration)
	if err := device.Stop(); err != nil {
		return result{}, err
	}
	if frames.Load() < uint64(duration.Milliseconds()*4) {
		return result{}, fmt.Errorf("audio callback produced too few frames: %d", frames.Load())
	}
	return result{
		OK: true, Version: helperVersion, CaptureFrames: frames.Load(),
		CapturePeak: peak.Load(), SampleRate: device.SampleRate(),
		CaptureChannels:  device.CaptureChannels(),
		PlaybackChannels: device.PlaybackChannels(),
	}, nil
}

func main() {
	mode := flag.String("mode", "list", "list, probe, bridge or stream")
	playbackID := flag.String("playback-id", "", "explicit playback endpoint id")
	captureID := flag.String("capture-id", "", "explicit capture endpoint id")
	durationMS := flag.Int("duration-ms", 500, "probe duration in milliseconds")
	mediaURL := flag.String("media-url", os.Getenv("MDD_MEDIA_URL"), "per-call media WebSocket URL")
	mediaToken := flag.String("media-token", os.Getenv("MDD_MEDIA_TOKEN"), "per-call media token")
	tlsPin := flag.String("tls-pin", os.Getenv("MDD_MEDIA_TLS_PIN"), "media WSS certificate SHA-256 pin")
	flag.Parse()

	backends, backendName := backendForPlatform()
	ctx, err := malgo.InitContext(backends, malgo.ContextConfig{}, nil)
	if err != nil {
		emit(result{OK: false, Version: helperVersion, Backend: backendName, Error: err.Error()})
		os.Exit(2)
	}
	defer ctx.Free()
	defer ctx.Uninit()

	switch *mode {
	case "list":
		devices, listErr := enumerate(ctx)
		if listErr != nil {
			emit(result{OK: false, Version: helperVersion, Backend: backendName, Error: listErr.Error()})
			os.Exit(2)
		}
		emit(result{OK: true, Version: helperVersion, Backend: backendName, Devices: devices})
	case "probe":
		if *playbackID == "" || *captureID == "" {
			emit(result{OK: false, Version: helperVersion, Backend: backendName,
				Error: "probe requires explicit playback-id and capture-id"})
			os.Exit(2)
		}
		duration := time.Duration(*durationMS) * time.Millisecond
		if duration < 200*time.Millisecond || duration > 5*time.Second {
			emit(result{OK: false, Version: helperVersion, Backend: backendName,
				Error: "duration-ms must be between 200 and 5000"})
			os.Exit(2)
		}
		value, probeErr := probe(ctx, *playbackID, *captureID, duration)
		value.Version, value.Backend = helperVersion, backendName
		if probeErr != nil {
			value.OK, value.Error = false, probeErr.Error()
			emit(value)
			os.Exit(3)
		}
		emit(value)
	case "bridge":
		if *playbackID == "" || *captureID == "" {
			emit(result{OK: false, Version: helperVersion, Backend: backendName,
				Error: "bridge requires explicit playback-id and capture-id"})
			os.Exit(2)
		}
		if bridgeErr := bridge(ctx, *playbackID, *captureID, *mediaURL, *mediaToken, *tlsPin); bridgeErr != nil {
			emit(result{OK: false, Version: helperVersion, Backend: backendName,
				Error: bridgeErr.Error()})
			os.Exit(4)
		}
	case "stream":
		if *playbackID == "" || *captureID == "" {
			emit(result{OK: false, Version: helperVersion, Backend: backendName,
				Error: "stream requires explicit playback-id and capture-id"})
			os.Exit(2)
		}
		if streamErr := stream(ctx, *playbackID, *captureID, backendName); streamErr != nil {
			_, _ = fmt.Fprintln(os.Stderr, streamErr)
			os.Exit(5)
		}
	default:
		emit(result{OK: false, Version: helperVersion, Backend: backendName,
			Error: "unsupported mode"})
		os.Exit(2)
	}
}
