package main

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gen2brain/malgo"
	"github.com/gorilla/websocket"
)

type pcmBuffer struct {
	mu   sync.Mutex
	data []byte
}

func (b *pcmBuffer) append(value []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	// Bound latency instead of accumulating old speech after a slow network interval.
	const maximum = 8000 * 2 * 2 // two seconds of 8 kHz S16 mono
	if len(value) >= maximum {
		b.data = append(b.data[:0], value[len(value)-maximum:]...)
		return
	}
	if overflow := len(b.data) + len(value) - maximum; overflow > 0 {
		copy(b.data, b.data[overflow:])
		b.data = b.data[:len(b.data)-overflow]
	}
	b.data = append(b.data, value...)
}

func (b *pcmBuffer) read(output []byte) int {
	clear(output)
	b.mu.Lock()
	defer b.mu.Unlock()
	count := copy(output, b.data)
	copy(b.data, b.data[count:])
	b.data = b.data[:len(b.data)-count]
	return count
}

type audioTelemetry struct {
	Type              string `json:"type"`
	CaptureCallbacks  uint64 `json:"capture_callbacks"`
	PlaybackCallbacks uint64 `json:"playback_callbacks"`
	CaptureBytes      uint64 `json:"capture_bytes"`
	PlaybackBytes     uint64 `json:"playback_bytes"`
}

func telemetrySnapshot(captureCallbacks, playbackCallbacks, captureBytes,
	playbackBytes *atomic.Uint64) audioTelemetry {
	return audioTelemetry{
		Type:              "audio.telemetry",
		CaptureCallbacks:  captureCallbacks.Load(),
		PlaybackCallbacks: playbackCallbacks.Load(),
		CaptureBytes:      captureBytes.Load(),
		PlaybackBytes:     playbackBytes.Load(),
	}
}

func normalizePin(value string) (string, error) {
	clean := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), ":", ""))
	if len(clean) != sha256.Size*2 {
		return "", errors.New("a SHA-256 TLS pin is required for a private WSS endpoint")
	}
	if _, err := hex.DecodeString(clean); err != nil {
		return "", errors.New("the TLS pin is not valid hexadecimal")
	}
	return clean, nil
}

func mediaDialer(rawURL, pin string) (*websocket.Dialer, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	dialer := *websocket.DefaultDialer
	if parsed.Scheme == "wss" {
		expected, pinErr := normalizePin(pin)
		if pinErr != nil {
			return nil, pinErr
		}
		dialer.TLSClientConfig = &tls.Config{ // #nosec G402 -- certificate is pinned below.
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: true,
			VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
				if len(rawCerts) == 0 {
					return errors.New("media WSS server supplied no certificate")
				}
				actual := fmt.Sprintf("%x", sha256.Sum256(rawCerts[0]))
				if actual != expected {
					return errors.New("media WSS certificate fingerprint mismatch")
				}
				return nil
			},
		}
	} else if parsed.Scheme != "ws" {
		return nil, errors.New("media URL must use ws or wss")
	}
	return &dialer, nil
}

func bridge(ctx *malgo.AllocatedContext, playbackID, captureID, mediaURL, token, pin string) error {
	if mediaURL == "" || token == "" {
		return errors.New("bridge requires media-url and a per-call token")
	}
	dialer, err := mediaDialer(mediaURL, pin)
	if err != nil {
		return err
	}
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+token)
	connection, response, err := dialer.Dial(mediaURL, headers)
	if err != nil {
		if response != nil {
			return fmt.Errorf("media WSS rejected the session: HTTP %d", response.StatusCode)
		}
		return err
	}
	defer connection.Close()
	connection.SetReadLimit(65536)

	playbacks, err := ctx.Devices(malgo.Playback)
	if err != nil {
		return err
	}
	captures, err := ctx.Devices(malgo.Capture)
	if err != nil {
		return err
	}
	playback, err := selectDevice(playbacks, playbackID)
	if err != nil {
		return fmt.Errorf("playback endpoint: %w", err)
	}
	capture, err := selectDevice(captures, captureID)
	if err != nil {
		return fmt.Errorf("capture endpoint: %w", err)
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

	downlink := &pcmBuffer{}
	uplink := make(chan []byte, 32)
	stopped := make(chan struct{}, 1)
	var captureCallbacks atomic.Uint64
	var playbackCallbacks atomic.Uint64
	var captureBytes atomic.Uint64
	var playbackBytes atomic.Uint64
	device, err := malgo.InitDevice(ctx.Context, config, malgo.DeviceCallbacks{
		Data: func(output, input []byte, _ uint32) {
			playbackCallbacks.Add(1)
			playbackBytes.Add(uint64(downlink.read(output)))
			if len(input) == 0 {
				return
			}
			captureCallbacks.Add(1)
			captureBytes.Add(uint64(len(input)))
			packet := append([]byte(nil), input...)
			select {
			case uplink <- packet:
			default: // Drop newest audio under backpressure; never block the real-time callback.
			}
		},
		Stop: func() {
			select {
			case stopped <- struct{}{}:
			default:
			}
		},
	})
	if err != nil {
		return err
	}
	defer device.Uninit()
	if err := device.Start(); err != nil {
		return err
	}
	defer device.Stop()

	errCh := make(chan error, 2)
	go func() {
		for {
			kind, payload, readErr := connection.ReadMessage()
			if readErr != nil {
				errCh <- readErr
				return
			}
			if kind != websocket.BinaryMessage || len(payload) == 0 {
				continue
			}
			downlink.append(payload)
		}
	}()
	go func() {
		ping := time.NewTicker(10 * time.Second)
		telemetry := time.NewTicker(500 * time.Millisecond)
		defer ping.Stop()
		defer telemetry.Stop()
		for {
			select {
			case payload := <-uplink:
				if writeErr := connection.WriteMessage(websocket.BinaryMessage, payload); writeErr != nil {
					errCh <- writeErr
					return
				}
			case <-ping.C:
				if writeErr := connection.WriteControl(
					websocket.PingMessage, nil, time.Now().Add(3*time.Second)); writeErr != nil {
					errCh <- writeErr
					return
				}
			case <-telemetry.C:
				payload, marshalErr := json.Marshal(telemetrySnapshot(
					&captureCallbacks, &playbackCallbacks, &captureBytes, &playbackBytes))
				if marshalErr != nil {
					errCh <- marshalErr
					return
				}
				if writeErr := connection.WriteMessage(websocket.TextMessage, payload); writeErr != nil {
					errCh <- writeErr
					return
				}
			}
		}
	}()

	emit(result{OK: true, Version: helperVersion, Backend: "media-bridge",
		SampleRate: device.SampleRate(), CaptureChannels: device.CaptureChannels(),
		PlaybackChannels: device.PlaybackChannels()})
	select {
	case err := <-errCh:
		return err
	case <-stopped:
		return errors.New("audio device stopped")
	}
}
