package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/gen2brain/malgo"
)

const modemUACPeriodFrames = 320 // 40 ms at 8 kHz; matches the proven EC20 UAC bridge

// stream exposes one exact modem UAC pair as raw 8 kHz S16LE mono PCM. stdin is
// playback toward the modem and stdout is capture from the modem. A single JSON
// line is emitted before the raw capture stream so the parent can fail closed if
// WASAPI did not negotiate the required format.
func stream(ctx *malgo.AllocatedContext, playbackID, captureID, backend string) error {
	playbacks, err := ctx.Devices(malgo.Playback)
	if err != nil {
		return err
	}
	captures, err := ctx.Devices(malgo.Capture)
	if err != nil {
		return err
	}
	playback, err := selectDevice(playbacks, playbackID)
	if err != nil {
		return fmt.Errorf("playback endpoint: %w", err)
	}
	capture, err := selectDevice(captures, captureID)
	if err != nil {
		return fmt.Errorf("capture endpoint: %w", err)
	}

	config := malgo.DefaultDeviceConfig(malgo.Duplex)
	config.SampleRate = 8000
	config.PeriodSizeInFrames = modemUACPeriodFrames
	config.Playback.DeviceID = playback.ID.Pointer()
	config.Playback.Format = malgo.FormatS16
	config.Playback.Channels = 1
	config.Capture.DeviceID = capture.ID.Pointer()
	config.Capture.Format = malgo.FormatS16
	config.Capture.Channels = 1

	downlink := &pcmBuffer{}
	uplink := make(chan []byte, 50) // one second; callback never blocks
	stopped := make(chan struct{}, 1)
	device, err := malgo.InitDevice(ctx.Context, config, malgo.DeviceCallbacks{
		Data: func(output, input []byte, _ uint32) {
			downlink.read(output)
			if len(input) == 0 {
				return
			}
			packet := append([]byte(nil), input...)
			select {
			case uplink <- packet:
			default:
				select {
				case <-uplink:
				default:
				}
				select {
				case uplink <- packet:
				default:
				}
			}
		},
		Stop: func() {
			select {
			case stopped <- struct{}{}:
			default:
			}
		},
	})
	if err != nil {
		return err
	}
	defer device.Uninit()
	if err := device.Start(); err != nil {
		return err
	}
	defer device.Stop()
	if device.SampleRate() != 8000 || device.CaptureChannels() != 1 || device.PlaybackChannels() != 1 {
		return errors.New("the UAC endpoint did not negotiate 8 kHz mono duplex PCM")
	}

	emit(result{OK: true, Version: helperVersion, Backend: backend,
		SampleRate: device.SampleRate(), CaptureChannels: device.CaptureChannels(),
		PlaybackChannels: device.PlaybackChannels()})
	errorsCh := make(chan error, 2)
	go func() {
		buffer := make([]byte, 1600)
		for {
			count, readErr := os.Stdin.Read(buffer)
			if count > 0 {
				downlink.append(buffer[:count])
			}
			if readErr != nil {
				errorsCh <- readErr
				return
			}
		}
	}()
	go func() {
		for packet := range uplink {
			if err := writeAll(os.Stdout, packet); err != nil {
				errorsCh <- err
				return
			}
		}
	}()

	select {
	case err := <-errorsCh:
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	case <-stopped:
		return errors.New("audio device stopped")
	}
}

func writeAll(target io.Writer, value []byte) error {
	for len(value) > 0 {
		count, err := target.Write(value)
		if err != nil {
			return err
		}
		if count == 0 {
			return io.ErrShortWrite
		}
		value = value[count:]
	}
	return nil
}
