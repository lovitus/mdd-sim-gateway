package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
)

func TestNormalizePin(t *testing.T) {
	plain := strings.Repeat("ab", 32)
	colon := strings.TrimSuffix(strings.Repeat("AB:", 32), ":")
	for _, input := range []string{plain, colon} {
		value, err := normalizePin(input)
		if err != nil || value != plain {
			t.Fatalf("normalizePin(%q) = %q, %v", input, value, err)
		}
	}
	if _, err := normalizePin("insecure"); err == nil {
		t.Fatal("invalid pin was accepted")
	}
}

func TestPCMBufferBoundsLatencyAndPadsSilence(t *testing.T) {
	buffer := &pcmBuffer{}
	input := bytes.Repeat([]byte{0x5a}, 8000*2*3)
	buffer.append(input)
	if len(buffer.data) != 8000*2*2 {
		t.Fatalf("buffer retained %d bytes", len(buffer.data))
	}
	retained := len(buffer.data)
	output := bytes.Repeat([]byte{0xff}, retained+16)
	buffer.read(output)
	if !bytes.Equal(output[:retained], bytes.Repeat([]byte{0x5a}, retained)) {
		t.Fatal("buffer did not preserve the newest audio")
	}
	if !bytes.Equal(output[retained:], make([]byte, 16)) {
		t.Fatal("short read was not padded with silence")
	}
}

func TestTelemetrySnapshotSchemaAndMonotonicValues(t *testing.T) {
	var captureCallbacks, playbackCallbacks, captureBytes, playbackBytes atomic.Uint64
	captureCallbacks.Store(2)
	playbackCallbacks.Store(3)
	captureBytes.Store(640)
	playbackBytes.Store(960)
	first := telemetrySnapshot(
		&captureCallbacks, &playbackCallbacks, &captureBytes, &playbackBytes)
	captureCallbacks.Add(1)
	playbackCallbacks.Add(1)
	captureBytes.Add(320)
	playbackBytes.Add(320)
	second := telemetrySnapshot(
		&captureCallbacks, &playbackCallbacks, &captureBytes, &playbackBytes)
	if first.Type != "audio.telemetry" || second.Type != "audio.telemetry" {
		t.Fatalf("unexpected telemetry type: %#v %#v", first, second)
	}
	if second.CaptureCallbacks <= first.CaptureCallbacks ||
		second.PlaybackCallbacks <= first.PlaybackCallbacks ||
		second.CaptureBytes <= first.CaptureBytes ||
		second.PlaybackBytes <= first.PlaybackBytes {
		t.Fatalf("telemetry did not grow monotonically: %#v -> %#v", first, second)
	}
	payload, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"type", "capture_callbacks", "playback_callbacks",
		"capture_bytes", "playback_bytes"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("missing telemetry field %q in %s", key, payload)
		}
	}
}
