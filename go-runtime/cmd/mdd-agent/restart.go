package main

import (
	"context"
	"log"
	"os"
	"os/exec"
)

func hostRestartCallback(settings config, hostMode string) func(context.Context) error {
	if settings.configPath == "" || !supervisedRestartAvailable(hostMode) {
		return nil
	}
	executable, err := os.Executable()
	if err != nil {
		return nil
	}
	return func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		// The helper must outlive the process it asks the supervisor to stop.
		command := exec.Command(executable, "service-restart", "--config", settings.configPath)
		if err := command.Start(); err != nil {
			return err
		}
		go func() {
			if err := command.Wait(); err != nil {
				log.Printf("Agent restart helper failed: %v", err)
			}
		}()
		return nil
	}
}
