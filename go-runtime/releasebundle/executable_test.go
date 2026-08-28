package releasebundle

import (
	"os"
	"runtime"
	"testing"
)

func TestInspectGoExecutable(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	version, err := InspectGoExecutable(executable, runtime.GOOS, runtime.GOARCH)
	if err != nil || version == "" {
		t.Fatalf("version=%q err=%v", version, err)
	}
	if _, err := InspectGoExecutable(executable, "invalid", runtime.GOARCH); err == nil {
		t.Fatal("wrong executable target was accepted")
	}
}
