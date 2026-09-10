package main

import (
	"errors"
	"flag"
	"io"
	"os"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentpolicy"
)

func runEnrollmentRepair(arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("repair-default-enrollment", flag.ContinueOnError)
	path := flags.String("store", "", "absolute path to the stopped Agent's backed-up modem policy database")
	equipment := flags.String("equipment", "", "confirmed test equipment identity")
	card := flags.String("card", "", "confirmed original card identity")
	firstSeen := flags.String("first-seen", "", "exact recorded first discovery timestamp")
	confirmed := flags.Bool("confirm", false, "confirm this equipment's default enrollment may resume")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	when, err := time.Parse(time.RFC3339Nano, *firstSeen)
	if err != nil || !*confirmed || flags.NArg() != 0 || *equipment == "" || *card == "" {
		return errors.New("explicit confirmation and exact equipment, card and first-seen are required")
	}
	info, err := os.Stat(*path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("existing regular policy database is required")
	}
	store, err := agentpolicy.Open(*path, time.Second)
	if err != nil {
		return err
	}
	defer store.Close()
	if err := store.RepairDefaultEnrollment(*equipment, *card, when); err != nil {
		return err
	}
	_, err = io.WriteString(output, "Default enrollment restored; device policy unchanged.\n")
	return err
}
