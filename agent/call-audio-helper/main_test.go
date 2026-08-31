package main

import "testing"

func TestBackendForPlatformRejectsCrossPlatformOverrides(t *testing.T) {
	if isWindows {
		backends, name, err := backendForPlatform("wasapi")
		if err != nil || len(backends) != 1 || name != "wasapi" {
			t.Fatalf("Windows backend=%v name=%q err=%v", backends, name, err)
		}
		if _, _, err := backendForPlatform("alsa"); err == nil {
			t.Fatal("Windows accepted ALSA")
		}
		return
	}
	if isDarwin {
		backends, name, err := backendForPlatform("coreaudio")
		if err != nil || len(backends) != 1 || name != "coreaudio" {
			t.Fatalf("macOS backend=%v name=%q err=%v", backends, name, err)
		}
		if _, _, err := backendForPlatform("alsa"); err == nil {
			t.Fatal("macOS accepted ALSA")
		}
		return
	}
	backends, name, err := backendForPlatform("alsa")
	if err != nil || len(backends) != 1 || name != "alsa" {
		t.Fatalf("Linux backend=%v name=%q err=%v", backends, name, err)
	}
	if _, _, err := backendForPlatform("coreaudio"); err == nil {
		t.Fatal("Linux accepted CoreAudio")
	}
}
