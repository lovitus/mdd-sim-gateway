package agentat

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type CallState struct {
	State             string
	Direction         string
	Number            string
	NativeIndex       int
	VoiceCalls        int
	IncomingCalls     int
	ObservedAt        time.Time
	Authoritative     bool
	TerminalConfirmed bool
	Strategy          string
}

// CallRecord is one voice entry from a fresh full AT+CLCC inventory. The
// native index is modem-owned and is only one part of the higher-level
// persistent incoming-call identity; it is never treated as globally unique.
type CallRecord struct {
	NativeIndex int
	State       string
	Direction   string
	Number      string
}

type CallInventory struct {
	Records       []CallRecord
	ObservedAt    time.Time
	Authoritative bool
}

type IncomingCallFence struct {
	NativeIndex int
	Number      string
}

type commandExchange func(context.Context, string, time.Duration) ([]byte, error)

var clccPattern = regexp.MustCompile(`\+CLCC:\s*(\d+),\s*(\d+),\s*(\d+),\s*(\d+),\s*\d+(?:,\s*"([^"]*)",\s*\d+)?`)
var qpcmvPattern = regexp.MustCompile(`(?i)\+QPCMV:\s*\([^)]*\)\s*,\s*\(([^)]*)\)`)

var ErrIncomingCallChanged = errors.New("incoming call identity changed")

func (owner *Owner) CallStatus(ctx context.Context) (CallState, error) {
	return readCallStatus(ctx, owner.Exchange)
}

func (owner *Owner) CallInventory(ctx context.Context) (CallInventory, error) {
	raw, err := owner.Exchange(ctx, "AT+CLCC", 5*time.Second)
	if err != nil {
		return CallInventory{}, err
	}
	return parseCLCCInventory(raw, time.Now())
}

func (owner *Owner) VerifiedHangup(ctx context.Context) (CallState, error) {
	return verifiedHangup(ctx, owner.Exchange, sleepContext)
}

func (owner *Owner) Dial(ctx context.Context, number string) (CallState, error) {
	if !safeTelephone(number) || number == "" {
		return CallState{}, errors.New("invalid telephone number")
	}
	if _, err := owner.Exchange(ctx, "ATD"+number+";", 15*time.Second); err != nil {
		return CallState{}, err
	}
	return owner.CallStatus(ctx)
}

func (owner *Owner) Answer(ctx context.Context) (CallState, error) {
	if _, err := owner.Exchange(ctx, "ATA", 15*time.Second); err != nil {
		return CallState{}, err
	}
	return owner.CallStatus(ctx)
}

// AnswerIncoming performs the final fresh native identity check and ATA while
// retaining the exclusive AT owner lock. Higher layers additionally verify
// the persistent event occurrence before entering this boundary.
func (owner *Owner) AnswerIncoming(ctx context.Context, expected IncomingCallFence) (CallState, error) {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	inventory, err := owner.callInventoryLocked(ctx)
	if err != nil {
		return CallState{}, err
	}
	if _, err := exactIncoming(inventory, expected); err != nil {
		return CallState{}, err
	}
	if _, err := owner.exchangeLocked(ctx, "ATA", 15*time.Second); err != nil {
		return CallState{}, err
	}
	for attempt := 0; attempt < 6; attempt++ {
		current, statusErr := owner.callInventoryLocked(ctx)
		if statusErr != nil {
			return CallState{}, statusErr
		}
		state, selectErr := selectCallState(current)
		if selectErr != nil {
			return CallState{}, selectErr
		}
		if state.State == "active" || state.State == "held" {
			if state.Direction != "in" || state.NativeIndex != expected.NativeIndex || state.VoiceCalls != 1 {
				return CallState{}, fmt.Errorf("%w while answering", ErrIncomingCallChanged)
			}
			return state, nil
		}
		if state.State == "idle" {
			return CallState{}, fmt.Errorf("%w before answer was confirmed", ErrIncomingCallChanged)
		}
		if err := sleepContext(ctx, 200*time.Millisecond); err != nil {
			return CallState{}, err
		}
	}
	return CallState{}, errors.New("incoming call answer was not confirmed")
}

// RejectIncoming may signal CHUP/ATH only while one fresh CLCC record exactly
// matches the selected incoming call. It cannot terminate a different active
// or waiting call and deliberately fails closed for multi-call inventories.
func (owner *Owner) RejectIncoming(ctx context.Context, expected IncomingCallFence) (CallState, error) {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	inventory, err := owner.callInventoryLocked(ctx)
	if err != nil {
		return CallState{}, err
	}
	if _, err := exactIncoming(inventory, expected); err != nil {
		return CallState{}, err
	}
	strategy := "incoming_chup"
	_, _ = owner.exchangeLocked(ctx, "AT+CHUP", 5*time.Second)
	for attempt := 0; attempt < 5; attempt++ {
		if err := sleepContext(ctx, 300*time.Millisecond); err != nil {
			return CallState{}, err
		}
		current, statusErr := owner.callInventoryLocked(ctx)
		if statusErr != nil {
			return CallState{}, statusErr
		}
		if len(current.Records) == 0 {
			return CallState{State: "idle", ObservedAt: current.ObservedAt, Authoritative: true,
				TerminalConfirmed: true, Strategy: strategy}, nil
		}
		if _, matchErr := exactIncoming(current, expected); matchErr != nil {
			return CallState{}, fmt.Errorf("%w while rejecting", ErrIncomingCallChanged)
		}
	}
	return CallState{}, errors.New("incoming call rejection was not terminally confirmed")
}

func (owner *Owner) callInventoryLocked(ctx context.Context) (CallInventory, error) {
	raw, err := owner.exchangeLocked(ctx, "AT+CLCC", 5*time.Second)
	if err != nil {
		return CallInventory{}, err
	}
	return parseCLCCInventory(raw, time.Now())
}

// SendDTMF emits one typed in-call tone. The active-call check and the
// restricted alphabet prevent this boundary from becoming a generic AT
// command channel.
func (owner *Owner) SendDTMF(ctx context.Context, signal string) (CallState, error) {
	signal = strings.ToUpper(strings.TrimSpace(signal))
	if len(signal) != 1 || !strings.Contains("0123456789*#ABCD", signal) {
		return CallState{}, errors.New("invalid DTMF signal")
	}
	status, err := owner.CallStatus(ctx)
	if err != nil {
		return CallState{}, err
	}
	if status.State != "active" {
		return CallState{}, errors.New("DTMF requires an active call")
	}
	if _, err := owner.Exchange(ctx, `AT+VTS="`+signal+`"`, 5*time.Second); err != nil {
		return CallState{}, err
	}
	return owner.CallStatus(ctx)
}

func (owner *Owner) EnableVoicePCM(ctx context.Context) error {
	return owner.EnableVoicePCMMode(ctx, 0)
}

func (owner *Owner) EnableVoicePCMMode(ctx context.Context, mode int) error {
	if !owner.capabilities.VoicePCM {
		return errors.New("modem does not advertise voice PCM")
	}
	if mode != 0 && mode != 2 {
		return errors.New("unsupported modem voice PCM mode")
	}
	_, err := owner.Exchange(ctx, fmt.Sprintf("AT+QPCMV=1,%d", mode), 5*time.Second)
	return err
}

func (owner *Owner) DisableVoicePCM(ctx context.Context) error {
	_, err := owner.Exchange(ctx, "AT+QPCMV=0", 5*time.Second)
	return err
}

func supportsVoicePCM(raw []byte) bool {
	match := qpcmvPattern.FindSubmatch(raw)
	if len(match) != 2 {
		return false
	}
	for _, token := range strings.Split(string(match[1]), ",") {
		token = strings.TrimSpace(token)
		if token == "0" || strings.HasPrefix(token, "0-") {
			return true
		}
	}
	return false
}

func readCallStatus(ctx context.Context, exchange commandExchange) (CallState, error) {
	raw, err := exchange(ctx, "AT+CLCC", 5*time.Second)
	if err != nil {
		return CallState{}, err
	}
	return parseCLCC(raw, time.Now())
}

func parseCLCC(raw []byte, observedAt time.Time) (CallState, error) {
	inventory, err := parseCLCCInventory(raw, observedAt)
	if err != nil {
		return CallState{}, err
	}
	return selectCallState(inventory)
}

func parseCLCCInventory(raw []byte, observedAt time.Time) (CallInventory, error) {
	matches := clccPattern.FindAllSubmatch(raw, -1)
	if len(matches) == 0 {
		return CallInventory{ObservedAt: observedAt, Authoritative: true, Records: []CallRecord{}}, nil
	}
	records := make([]CallRecord, 0, len(matches))
	states := map[int]string{0: "active", 1: "held", 2: "dialing", 3: "ringing_out", 4: "ringing_in", 5: "waiting"}
	for _, match := range matches {
		index, indexErr := strconv.Atoi(string(match[1]))
		direction, directionErr := strconv.Atoi(string(match[2]))
		state, stateErr := strconv.Atoi(string(match[3]))
		mode, modeErr := strconv.Atoi(string(match[4]))
		if indexErr != nil || index < 1 || directionErr != nil || stateErr != nil || modeErr != nil {
			return CallInventory{}, errors.New("invalid +CLCC numeric fields")
		}
		if mode != 0 {
			continue
		}
		stateName := states[state]
		if stateName == "" {
			return CallInventory{}, fmt.Errorf("unsupported +CLCC state %d", state)
		}
		directionName := "in"
		if direction == 0 {
			directionName = "out"
		} else if direction != 1 {
			return CallInventory{}, fmt.Errorf("unsupported +CLCC direction %d", direction)
		}
		number := strings.TrimSpace(string(match[5]))
		if !safeTelephone(number) {
			number = ""
		}
		records = append(records, CallRecord{NativeIndex: index, State: stateName, Direction: directionName, Number: number})
	}
	sort.Slice(records, func(left, right int) bool { return records[left].NativeIndex < records[right].NativeIndex })
	return CallInventory{Records: records, ObservedAt: observedAt, Authoritative: true}, nil
}

func selectCallState(inventory CallInventory) (CallState, error) {
	if !inventory.Authoritative || inventory.ObservedAt.IsZero() {
		return CallState{}, errors.New("call inventory is not authoritative")
	}
	if len(inventory.Records) == 0 {
		return CallState{State: "idle", ObservedAt: inventory.ObservedAt, Authoritative: true}, nil
	}
	priority := map[string]int{"active": 0, "held": 1, "waiting": 2, "ringing_in": 3, "ringing_out": 4, "dialing": 5}
	records := append([]CallRecord(nil), inventory.Records...)
	sort.SliceStable(records, func(left, right int) bool {
		leftPriority, leftKnown := priority[records[left].State]
		rightPriority, rightKnown := priority[records[right].State]
		if !leftKnown {
			leftPriority = 99
		}
		if !rightKnown {
			rightPriority = 99
		}
		return leftPriority < rightPriority
	})
	selected := records[0]
	incoming := 0
	for _, record := range records {
		if record.Direction == "in" && (record.State == "ringing_in" || record.State == "waiting") {
			incoming++
		}
	}
	return CallState{
		State: selected.State, Direction: selected.Direction, Number: selected.Number,
		NativeIndex: selected.NativeIndex, VoiceCalls: len(records), IncomingCalls: incoming,
		ObservedAt: inventory.ObservedAt, Authoritative: true,
	}, nil
}

func exactIncoming(inventory CallInventory, expected IncomingCallFence) (CallRecord, error) {
	if !inventory.Authoritative || expected.NativeIndex < 1 || len(inventory.Records) != 1 {
		return CallRecord{}, fmt.Errorf("%w: identity is ambiguous", ErrIncomingCallChanged)
	}
	record := inventory.Records[0]
	if record.NativeIndex != expected.NativeIndex || record.Direction != "in" || record.State != "ringing_in" {
		return CallRecord{}, ErrIncomingCallChanged
	}
	expectedNumber := strings.TrimSpace(expected.Number)
	if expectedNumber != "" && record.Number != "" && record.Number != expectedNumber {
		return CallRecord{}, fmt.Errorf("%w: peer changed", ErrIncomingCallChanged)
	}
	return record, nil
}

func verifiedHangup(ctx context.Context, exchange commandExchange, sleep func(context.Context, time.Duration) error) (CallState, error) {
	deadline := time.Now().Add(25 * time.Second)
	status := func() (CallState, error) {
		if err := ctx.Err(); err != nil {
			return CallState{}, err
		}
		if time.Now().After(deadline) {
			return CallState{}, context.DeadlineExceeded
		}
		return readCallStatus(ctx, exchange)
	}
	confirmIdle := func(first CallState) (CallState, bool) {
		if first.State != "idle" || !first.Authoritative {
			return CallState{}, false
		}
		if err := sleep(ctx, 400*time.Millisecond); err != nil {
			return CallState{}, false
		}
		second, err := status()
		return second, err == nil && second.State == "idle" && second.Authoritative
	}
	initial, err := status()
	if err != nil {
		return CallState{}, err
	}
	if terminal, ok := confirmIdle(initial); ok {
		terminal.TerminalConfirmed = true
		terminal.Strategy = "already_idle"
		return terminal, nil
	}
	last := initial
	strategy := "chup"
	for _, command := range []string{"AT+CHUP", "ATH"} {
		if command == "ATH" {
			strategy = "chup_ath"
		}
		_, _ = exchange(ctx, command, 5*time.Second)
		for attempt := 0; attempt < 5; attempt++ {
			current, statusErr := status()
			if statusErr == nil {
				last = current
				if terminal, ok := confirmIdle(current); ok {
					terminal.TerminalConfirmed = true
					terminal.Strategy = strategy
					return terminal, nil
				}
			}
			if err := sleep(ctx, 400*time.Millisecond); err != nil {
				return CallState{}, err
			}
		}
	}
	return CallState{}, fmt.Errorf("physical call termination was not confirmed; last state %s", last.State)
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func safeTelephone(value string) bool {
	if value == "" {
		return true
	}
	if len(value) > 64 {
		return false
	}
	digits := 0
	for index, character := range value {
		if character >= '0' && character <= '9' {
			digits++
			continue
		}
		if character == '+' && index == 0 {
			continue
		}
		return false
	}
	return digits > 0
}
