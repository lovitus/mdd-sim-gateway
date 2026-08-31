//go:build linux || windows

package rawusb

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/sagernet/sing/common/logger"
)

// dependencyLogger exposes only bounded lifecycle diagnostics from sing-usbip.
// Its inputs contain USB identities and attach errors, not WSS credentials or
// payload bytes. Trace/debug remain disabled to avoid per-URB log volume.
type dependencyLogger struct{ component string }

func (current dependencyLogger) write(level string, values ...any) {
	detail := strings.ToValidUTF8(fmt.Sprint(values...), "?")
	if len(detail) > 1024 {
		detail = strings.ToValidUTF8(detail[:1024], "?")
	}
	log.Printf("mdd-agent: %s %s: %s", current.component, level, detail)
}

func (dependencyLogger) Trace(...any) {}
func (dependencyLogger) Debug(...any) {}
func (current dependencyLogger) Info(values ...any) {
	current.write("info", values...)
}
func (current dependencyLogger) Warn(values ...any) {
	current.write("warning", values...)
}
func (current dependencyLogger) Error(values ...any) {
	current.write("error", values...)
}
func (current dependencyLogger) Fatal(values ...any) { current.write("fatal", values...) }
func (current dependencyLogger) Panic(values ...any) { current.write("panic", values...) }

func (dependencyLogger) TraceContext(context.Context, ...any) {}
func (dependencyLogger) DebugContext(context.Context, ...any) {}
func (current dependencyLogger) InfoContext(_ context.Context, values ...any) {
	current.Info(values...)
}
func (current dependencyLogger) WarnContext(_ context.Context, values ...any) {
	current.Warn(values...)
}
func (current dependencyLogger) ErrorContext(_ context.Context, values ...any) {
	current.Error(values...)
}
func (current dependencyLogger) FatalContext(_ context.Context, values ...any) {
	current.Fatal(values...)
}
func (current dependencyLogger) PanicContext(_ context.Context, values ...any) {
	current.Panic(values...)
}

var _ logger.ContextLogger = dependencyLogger{}
