package providerdeploy

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// VerifyFixedImage reads the live Linux process, never the replaced disk link.
// A service becoming active without running the expected image is not accepted.
func (manager Systemctl) VerifyFixedImage(ctx context.Context, unit, digest string) error {
	switch unit {
	case "mdd-core.service", "mdd-provider-apply.service", "mdd-egress.service":
	default:
		return errors.New("invalid fixed update service")
	}
	output, err := manager.output(ctx, "show", unit, "--property=MainPID", "--property=ActiveState")
	if err != nil {
		return err
	}
	pid := 0
	active := false
	for _, line := range strings.Split(output, "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if key == "MainPID" {
			pid, _ = strconv.Atoi(value)
		}
		if key == "ActiveState" {
			active = value == "active"
		}
	}
	if pid < 1 || !active {
		return errors.New("fixed update service is not active")
	}
	file, err := os.Open(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return err
	}
	if fmt.Sprintf("%x", hash.Sum(nil)) != digest {
		return errors.New("fixed update service executable does not match target")
	}
	confirmation, err := manager.output(ctx, "show", unit, "--property=MainPID", "--property=ActiveState")
	if err != nil {
		return err
	}
	if confirmation != output {
		return errors.New("fixed update service changed during health readback")
	}
	return nil
}
