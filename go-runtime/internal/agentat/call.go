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
	ObservedAt        time.Time
	Authoritative     bool
	TerminalConfirmed bool
	Strategy          string
}

type commandExchange func(context.Context, string, time.Duration) ([]byte, error)

var clccPattern = regexp.MustCompile(`\+CLCC:\s*\d+,\s*(\d+),\s*(\d+),\s*(\d+),\s*\d+(?:,\s*"([^"]*)",\s*\d+)?`)
var qpcmvPattern = regexp.MustCompile(`(?i)\+QPCMV:\s*\([^)]*\)\s*,\s*\(([^)]*)\)`)

func (owner *Owner) CallStatus(ctx context.Context) (CallState, error) {
	return readCallStatus(ctx, owner.Exchange)
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

func (owner *Owner) EnableVoicePCM(ctx context.Context) error {
	if !owner.capabilities.VoicePCM {
		return errors.New("modem does not advertise serial voice PCM")
	}
	_, err := owner.Exchange(ctx, "AT+QPCMV=1,0", 5*time.Second)
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
	matches := clccPattern.FindAllSubmatch(raw, -1)
	if len(matches) == 0 {
		return CallState{State: "idle", ObservedAt: observedAt, Authoritative: true}, nil
	}
	type record struct {
		direction int
		state     int
		number    string
	}
	records := make([]record, 0, len(matches))
	for _, match := range matches {
		direction, directionErr := strconv.Atoi(string(match[1]))
		state, stateErr := strconv.Atoi(string(match[2]))
		mode, modeErr := strconv.Atoi(string(match[3]))
		if directionErr != nil || stateErr != nil || modeErr != nil {
			return CallState{}, errors.New("invalid +CLCC numeric fields")
		}
		if mode != 0 {
			continue
		}
		number := strings.TrimSpace(string(match[4]))
		if !safeTelephone(number) {
			number = ""
		}
		records = append(records, record{direction: direction, state: state, number: number})
	}
	if len(records) == 0 {
		return CallState{State: "idle", ObservedAt: observedAt, Authoritative: true}, nil
	}
	priority := map[int]int{0: 0, 1: 1, 5: 2, 4: 3, 3: 4, 2: 5}
	sort.SliceStable(records, func(left, right int) bool {
		leftPriority, leftKnown := priority[records[left].state]
		rightPriority, rightKnown := priority[records[right].state]
		if !leftKnown {
			leftPriority = 99
		}
		if !rightKnown {
			rightPriority = 99
		}
		return leftPriority < rightPriority
	})
	selected := records[0]
	states := map[int]string{0: "active", 1: "held", 2: "dialing", 3: "ringing_out", 4: "ringing_in", 5: "waiting"}
	state := states[selected.state]
	if state == "" {
		return CallState{}, fmt.Errorf("unsupported +CLCC state %d", selected.state)
	}
	direction := "in"
	if selected.direction == 0 {
		direction = "out"
	} else if selected.direction != 1 {
		return CallState{}, fmt.Errorf("unsupported +CLCC direction %d", selected.direction)
	}
	return CallState{
		State: state, Direction: direction, Number: selected.number,
		ObservedAt: observedAt, Authoritative: true,
	}, nil
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
