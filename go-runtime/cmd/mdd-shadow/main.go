// mdd-shadow projects a saved legacy /api/snapshot response through the new
// state model. It intentionally has no URL, token, or mutation flags.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/legacy"
)

const maxSnapshotBytes = 64 << 20

type output struct {
	Mode        string                  `json:"mode"`
	GeneratedAt time.Time               `json:"generated_at"`
	Lines       []legacy.LineProjection `json:"lines"`
}

func main() {
	snapshotPath := flag.String("snapshot", "", "path to a saved legacy /api/snapshot JSON file")
	ttl := flag.Duration("ttl", 30*time.Second, "freshness lifetime for the locally received snapshot")
	flag.Parse()
	if *snapshotPath == "" {
		fatalf("-snapshot is required")
	}
	file, err := os.Open(*snapshotPath)
	if err != nil {
		fatalf("open snapshot: %v", err)
	}
	defer file.Close()
	snapshot, err := legacy.Decode(io.LimitReader(file, maxSnapshotBytes+1))
	if err != nil {
		fatalf("decode snapshot: %v", err)
	}
	if info, err := file.Stat(); err == nil && info.Size() > maxSnapshotBytes {
		fatalf("snapshot exceeds %d bytes", maxSnapshotBytes)
	}
	translator, err := legacy.NewTranslator(*ttl)
	if err != nil {
		fatalf("create translator: %v", err)
	}
	now := time.Now().UTC()
	lines, err := translator.Translate(snapshot, now)
	if err != nil {
		fatalf("translate snapshot: %v", err)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(output{Mode: "read_only_shadow", GeneratedAt: now, Lines: lines}); err != nil {
		fatalf("encode output: %v", err)
	}
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "mdd-shadow: "+format+"\n", args...)
	os.Exit(2)
}
