//go:build linux

package linuxmodem

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestAudioHelperJSONIsIndependentOfPluginWarnings(t *testing.T) {
	const valid = `{"ok":true,"version":4,"backend":"alsa","devices":[{"kind":"playback","name":"EC20-CE-HDLG, USB Audio","id":"3a312c30"},{"kind":"capture","name":"EC20-CE-HDLG, USB Audio","id":"3a312c30"}]}`
	for _, test := range []struct {
		name, stdout, stderr string
		exit                 string
		wantOK               bool
	}{
		{"quiet", valid, "", "0", true},
		{"optional_pulse_warning", valid, "Failed to create secure directory (/run/user/0/pulse): No such file or directory", "0", true},
		{"nonzero_even_with_valid_json", valid, "device failed", "1", false},
		{"json_on_stderr_is_not_protocol", "", valid, "0", false},
		{"malformed_stdout", "not-json", "", "0", false},
		{"old_version", `{"ok":true,"version":3,"backend":"alsa"}`, "", "0", false},
		{"wrong_backend", `{"ok":true,"version":4,"backend":"null"}`, "", "0", false},
		{"negative_result", `{"ok":false,"version":4,"backend":"alsa"}`, "", "0", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			helper := filepath.Join(t.TempDir(), "audio-helper")
			script := "#!/bin/sh\nprintf '%s\\n' '" + test.stdout + "'\nprintf '%s\\n' '" + test.stderr + "' >&2\nexit " + test.exit + "\n"
			if err := os.WriteFile(helper, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			result, err := runLinuxAudioHelper(context.Background(), helper, "-backend", "alsa", "-mode", "list")
			if (err == nil) != test.wantOK {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if test.wantOK && (len(result.Devices) != 2 || result.Devices[0].ID != "3a312c30" || result.Devices[1].Kind != "capture") {
				t.Fatalf("lost exact ALSA endpoints: %+v", result.Devices)
			}
		})
	}
}
