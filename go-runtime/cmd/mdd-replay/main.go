// mdd-replay applies saved Go state-event records without contacting or
// mutating a live MDD deployment.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/events"
)

type output struct {
	Mode  string                  `json:"mode"`
	At    time.Time               `json:"at"`
	Lines []events.LineProjection `json:"lines"`
}

func main() {
	eventsPath := flag.String("events", "", "path to saved MDD Go state records in NDJSON format")
	ttl := flag.Duration("ttl", 30*time.Second, "fact freshness lifetime")
	flag.Parse()
	if *eventsPath == "" {
		fatalf("-events is required")
	}
	file, err := os.Open(*eventsPath)
	if err != nil {
		fatalf("open events: %v", err)
	}
	defer file.Close()
	replay, err := events.NewReplay(*ttl)
	if err != nil {
		fatalf("create replay: %v", err)
	}
	if err := events.ReadJSONLines(file, replay, events.DefaultMaxRecordBytes); err != nil {
		fatalf("replay events: %v", err)
	}
	at := replay.LastReceivedAt()
	if at.IsZero() {
		fatalf("event file contains no records")
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(output{Mode: "read_only_replay", At: at, Lines: replay.Projections(at)}); err != nil {
		fatalf("encode output: %v", err)
	}
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "mdd-replay: "+format+"\n", args...)
	os.Exit(2)
}
