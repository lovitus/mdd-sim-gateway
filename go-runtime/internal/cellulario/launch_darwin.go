//go:build darwin

package cellulario

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func Launch(ctx context.Context, executable string, attachment Attachment) (*Client, error) {
	info, err := os.Stat(executable)
	if err != nil || info.IsDir() || info.Mode()&0111 == 0 {
		return nil, errors.New("bundled cellular companion is unavailable")
	}
	sockets, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		return nil, err
	}
	parent := os.NewFile(uintptr(sockets[0]), "mdd-cellular-parent")
	child := os.NewFile(uintptr(sockets[1]), "mdd-cellular-child")
	watchRead, watchWrite, err := os.Pipe()
	if err != nil {
		_ = parent.Close()
		_ = child.Close()
		return nil, err
	}
	command := exec.Command(executable,
		"--ipc-fd", "3", "--watch-fd", "4",
		"--vid", strconv.FormatUint(uint64(attachment.VID), 10),
		"--pid", strconv.FormatUint(uint64(attachment.PID), 10),
		"--bus", strconv.FormatUint(uint64(attachment.Bus), 10),
		"--address", strconv.FormatUint(uint64(attachment.Address), 10),
	)
	command.ExtraFiles = []*os.File{child, watchRead}
	command.Stdin, command.Stdout = nil, nil
	stderr, err := command.StderrPipe()
	if err != nil {
		_ = parent.Close()
		_ = child.Close()
		_ = watchRead.Close()
		_ = watchWrite.Close()
		return nil, err
	}
	if err := command.Start(); err != nil {
		_ = parent.Close()
		_ = child.Close()
		_ = watchRead.Close()
		_ = watchWrite.Close()
		return nil, err
	}
	_ = child.Close()
	_ = watchRead.Close()
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		scanner := bufio.NewScanner(stderr)
		scanner.Buffer(make([]byte, 1024), 64*1024)
		for scanner.Scan() {
			if detail := boundedText(scanner.Text(), 1000); detail != "" {
				log.Printf("mdd-cellular-io: %s", detail)
			}
		}
	}()
	cleanup := func() error {
		watchErr := watchWrite.Close()
		wait := make(chan error, 1)
		go func() { wait <- command.Wait() }()
		var processErr error
		select {
		case processErr = <-wait:
		case <-time.After(8 * time.Second):
			_ = command.Process.Signal(syscall.SIGTERM)
			select {
			case processErr = <-wait:
			case <-time.After(3 * time.Second):
				killErr := command.Process.Kill()
				processErr = errors.Join(killErr, <-wait)
			}
		}
		select {
		case <-stderrDone:
		case <-time.After(time.Second):
		}
		if exit := new(exec.ExitError); errors.As(processErr, &exit) &&
			(strings.Contains(processErr.Error(), "signal: terminated") || strings.Contains(processErr.Error(), "signal: killed")) {
			processErr = nil
		}
		return errors.Join(watchErr, processErr)
	}
	client, err := newClient(parent, cleanup)
	if err != nil {
		_ = parent.Close()
		_ = cleanup()
		return nil, err
	}
	initializeContext, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := client.initialize(initializeContext); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("initialize cellular companion: %w", err)
	}
	return client, nil
}
