// Package pcscmonitor adapts PC/SC reader changes to agentreader.Monitor.
// Reader names identify only a live attachment. Durable card identity is read
// by the session layer from EID/ICCID/profile data after a card is connected.
package pcscmonitor

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/ebfe/scard"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentreader"
)

type pcscContext interface {
	ListReaders() ([]string, error)
	GetStatusChange([]scard.ReaderState, time.Duration) error
	Cancel() error
	Release() error
}

type Factory struct{}

func (Factory) Open(ctx context.Context) (agentreader.Monitor, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	pcsc, err := scard.EstablishContext()
	if err != nil {
		return nil, fmt.Errorf("establish PC/SC context: %w", err)
	}
	monitorID, err := randomID()
	if err != nil {
		_ = pcsc.Release()
		return nil, err
	}
	return newMonitor(pcsc, monitorID), nil
}

type cardState struct {
	present      bool
	atrDigest    [sha256.Size]byte
	eventCounter uint16
	generation   string
}

type Monitor struct {
	pcsc pcscContext
	id   string

	states         map[string]scard.StateFlag
	cards          map[string]cardState
	nextGeneration uint64
	closeOnce      sync.Once
	closeErr       error
}

func newMonitor(pcsc pcscContext, id string) *Monitor {
	return &Monitor{
		pcsc: pcsc, id: id,
		states: make(map[string]scard.StateFlag),
		cards:  make(map[string]cardState),
	}
}

func (monitor *Monitor) Scan(ctx context.Context) ([]agentreader.Reader, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for attempt := 0; attempt < 2; attempt++ {
		readers, err := monitor.scanOnce()
		if err == nil {
			return readers, nil
		}
		if !errors.Is(err, scard.ErrUnknownReader) && !errors.Is(err, scard.ErrReaderUnavailable) {
			return nil, err
		}
	}
	return nil, errors.New("PC/SC readers changed during two consecutive snapshots")
}

func (monitor *Monitor) scanOnce() ([]agentreader.Reader, error) {
	names, err := monitor.pcsc.ListReaders()
	if errors.Is(err, scard.ErrNoReadersAvailable) {
		monitor.states = make(map[string]scard.StateFlag)
		monitor.cards = make(map[string]cardState)
		return []agentreader.Reader{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list PC/SC readers: %w", err)
	}
	if err := validateNames(names); err != nil {
		return nil, err
	}
	states := make([]scard.ReaderState, len(names))
	for index, name := range names {
		states[index] = scard.ReaderState{Reader: name, CurrentState: scard.StateUnaware}
	}
	if len(states) > 0 {
		if err := monitor.pcsc.GetStatusChange(states, 0); err != nil {
			return nil, fmt.Errorf("snapshot PC/SC readers: %w", err)
		}
	}
	return monitor.update(states), nil
}

func (monitor *Monitor) Wait(ctx context.Context, _ []agentreader.Reader, maximum time.Duration) error {
	if maximum <= 0 {
		return errors.New("PC/SC wait maximum must be positive")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(monitor.states) == 0 {
		timer := time.NewTimer(maximum)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		}
	}
	names := make([]string, 0, len(monitor.states))
	for name := range monitor.states {
		names = append(names, name)
	}
	sort.Strings(names)
	states := make([]scard.ReaderState, len(names))
	for index, name := range names {
		states[index] = scard.ReaderState{Reader: name, CurrentState: monitor.states[name]}
	}

	cancelDone := make(chan struct{})
	stopCancel := context.AfterFunc(ctx, func() {
		defer close(cancelDone)
		_ = monitor.pcsc.Cancel()
	})
	err := monitor.pcsc.GetStatusChange(states, maximum)
	if !stopCancel() {
		<-cancelDone
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, scard.ErrTimeout) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("wait for PC/SC change: %w", err)
	}
	for _, state := range states {
		monitor.states[state.Reader] = state.EventState
	}
	return nil
}

func (monitor *Monitor) Close() error {
	monitor.closeOnce.Do(func() {
		monitor.closeErr = errors.Join(monitor.pcsc.Cancel(), monitor.pcsc.Release())
	})
	return monitor.closeErr
}

func (monitor *Monitor) update(states []scard.ReaderState) []agentreader.Reader {
	currentNames := make(map[string]struct{}, len(states))
	readers := make([]agentreader.Reader, 0, len(states))
	for _, state := range states {
		currentNames[state.Reader] = struct{}{}
		monitor.states[state.Reader] = state.EventState
		present := state.EventState&scard.StatePresent != 0 &&
			state.EventState&(scard.StateUnknown|scard.StateUnavailable|scard.StateEmpty) == 0
		counter := uint16(uint32(state.EventState) >> 16)
		digest := sha256.Sum256(state.Atr)
		previous := monitor.cards[state.Reader]
		changedWhilePresent := previous.present && present &&
			(previous.atrDigest != digest || (counter != 0 && previous.eventCounter != 0 && counter != previous.eventCounter))
		generation := previous.generation
		if present && (!previous.present || changedWhilePresent) {
			monitor.nextGeneration++
			generation = fmt.Sprintf("%s:%d", monitor.id, monitor.nextGeneration)
		}
		if !present {
			generation = ""
		}
		monitor.cards[state.Reader] = cardState{
			present: present, atrDigest: digest, eventCounter: counter, generation: generation,
		}
		readers = append(readers, agentreader.Reader{
			Name: state.Reader, CardPresent: present, SessionGeneration: generation,
			ATR: append([]byte(nil), state.Atr...),
		})
	}
	for name := range monitor.states {
		if _, exists := currentNames[name]; !exists {
			delete(monitor.states, name)
			delete(monitor.cards, name)
		}
	}
	return readers
}

func validateNames(names []string) error {
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if name == "" {
			return errors.New("PC/SC returned an empty reader name")
		}
		if _, duplicate := seen[name]; duplicate {
			return fmt.Errorf("PC/SC returned duplicate reader attachment %q", name)
		}
		seen[name] = struct{}{}
	}
	return nil
}

func randomID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("create PC/SC monitor generation: %w", err)
	}
	return hex.EncodeToString(value), nil
}
