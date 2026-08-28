package main

import "testing"

func TestReleaseCommandRequiresCompleteInputs(t *testing.T) {
	if err := run(nil); err == nil {
		t.Fatal("empty release command was accepted")
	}
}
