package windowspcm

import (
	"encoding/hex"
	"errors"
	"testing"
	"unicode/utf16"
)

func encodedDeviceID(value string) string {
	words := utf16.Encode([]rune(value))
	raw := make([]byte, len(words)*2)
	for index, word := range words {
		raw[index*2], raw[index*2+1] = byte(word), byte(word>>8)
	}
	return hex.EncodeToString(raw)
}

func TestSelectUACRequiresExactHealthyEndpointPair(t *testing.T) {
	endpoints := []uacEndpoint{
		{Kind: "playback", InstanceID: `SWD\MMDEVAPI\{0.0.0.00000000}.PLAY`, Status: "OK"},
		{Kind: "capture", InstanceID: `SWD\MMDEVAPI\{0.0.1.00000000}.CAP`, Status: "OK"},
		{Kind: "capture", InstanceID: `SWD\MMDEVAPI\OTHER`, Status: "Error"},
	}
	devices := []helperDevice{
		{Kind: "capture", ID: encodedDeviceID(`{0.0.1.00000000}.CAP`)},
		{Kind: "playback", ID: encodedDeviceID(`{0.0.0.00000000}.PLAY`)},
		{Kind: "capture", ID: encodedDeviceID(`OTHER`)},
	}
	playback, capture, err := selectUAC(endpoints, devices)
	if err != nil || playback != devices[1].ID || capture != devices[0].ID {
		t.Fatalf("playback=%q capture=%q err=%v", playback, capture, err)
	}
}

func TestSelectUACRejectsMissingAndAmbiguousEndpoints(t *testing.T) {
	endpoints := []uacEndpoint{{Kind: "playback", InstanceID: `SWD\MMDEVAPI\PLAY`, Status: "OK"}}
	if _, _, err := selectUAC(endpoints, []helperDevice{{Kind: "playback", ID: encodedDeviceID("PLAY")}}); !errors.Is(err, ErrUACUnavailable) {
		t.Fatalf("missing pair error=%v", err)
	}
	devices := []helperDevice{
		{Kind: "playback", ID: encodedDeviceID("PLAY")},
		{Kind: "playback", ID: encodedDeviceID("PLAY") + "00"},
	}
	if _, _, err := selectUAC(endpoints, devices); !errors.Is(err, ErrUACAmbiguous) {
		t.Fatalf("ambiguous error=%v", err)
	}
}
