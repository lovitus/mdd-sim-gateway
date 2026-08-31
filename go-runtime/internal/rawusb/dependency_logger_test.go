//go:build linux || windows

package rawusb

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

func TestDependencyLoggerBoundsLifecycleDiagnostics(t *testing.T) {
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(previous) })

	(dependencyLogger{component: "sing-usbip importer"}).Error(strings.Repeat("x", 2048))
	line := output.String()
	if !strings.Contains(line, "sing-usbip importer error:") || len(line) > 1200 {
		t.Fatalf("unbounded or unidentified dependency log: length=%d", len(line))
	}
}
