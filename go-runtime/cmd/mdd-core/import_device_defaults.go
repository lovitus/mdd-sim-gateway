package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/systempreferences"
)

func runDeviceDefaultsImport(arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("import-device-defaults", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	source := flags.String("source", "", "legacy device desired.json")
	destination := flags.String("preferences", "", "Go preferences database")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if *source == "" || *destination == "" || flags.NArg() != 0 {
		return errors.New("source and preferences paths are required")
	}
	file, err := os.Open(*source)
	if err != nil {
		return err
	}
	defer file.Close()
	payload, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil {
		return err
	}
	value, err := systempreferences.ReadLegacyDefaults(payload)
	if err != nil {
		return err
	}
	store, err := systempreferences.Open(*destination, 5*time.Second)
	if err != nil {
		return err
	}
	defer store.Close()
	saved, created, err := store.ImportLegacyDefaults(value)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(payload)
	return json.NewEncoder(output).Encode(map[string]any{"status": "imported", "created": created, "revision": saved.Revision, "source_sha256": hex.EncodeToString(digest[:])})
}
