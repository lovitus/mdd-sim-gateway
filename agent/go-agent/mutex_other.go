//go:build !windows

package main

func acquireUnifiedAgentLease() bool { return true }
