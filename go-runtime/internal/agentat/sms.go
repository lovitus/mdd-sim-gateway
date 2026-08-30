package agentat

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/warthog618/sms"
	"github.com/warthog618/sms/encoding/pdumode"
	"github.com/warthog618/sms/encoding/tpdu"
)

const maximumSMSParts = 7

var (
	smsListHeader = regexp.MustCompile(`(?m)^\+CMGL:\s*(\d+)\s*,\s*(\d+)[^\r\n]*\r?\n([0-9A-Fa-f]+)\s*$`)
	smsReference  = regexp.MustCompile(`(?m)^\+CMGS:\s*(\d+)\s*$`)
)

type SMSMessage struct {
	Index         int
	State         string
	Direction     string
	Peer          string
	Body          string
	ObservedAt    time.Time
	Fingerprint   string
	Reference     int
	DeliveryState string
}

type SMSSubmitError struct {
	PossiblySent bool
	Err          error
}

func (failure *SMSSubmitError) Error() string         { return failure.Err.Error() }
func (failure *SMSSubmitError) Unwrap() error         { return failure.Err }
func (failure *SMSSubmitError) PossiblySentSMS() bool { return failure.PossiblySent }

func SMSPossiblySent(err error) bool {
	var failure *SMSSubmitError
	return errors.As(err, &failure) && failure.PossiblySent
}

func (owner *Owner) ListSMS(ctx context.Context, equipmentID string) ([]SMSMessage, error) {
	if owner.equipmentID != equipmentID || !owner.capabilities.SMS {
		return nil, errors.New("SMS operation target is unavailable")
	}
	if _, err := owner.Exchange(ctx, "AT+CMGF=0", 3*time.Second); err != nil {
		return nil, err
	}
	response, err := owner.Exchange(ctx, "AT+CMGL=4", 30*time.Second)
	if err != nil {
		return nil, err
	}
	return decodeSMSList(response, time.Now().UTC())
}

func (owner *Owner) SendSMS(ctx context.Context, equipmentID, recipient, body string) ([]int, error) {
	if owner.equipmentID != equipmentID || !owner.capabilities.SMS || !safeTelephone(recipient) || strings.TrimSpace(body) == "" {
		return nil, errors.New("invalid SMS submission")
	}
	pdus, err := sms.Encode([]byte(body), sms.To(recipient), sms.WithAllCharsets)
	if err != nil {
		return nil, err
	}
	if len(pdus) < 1 || len(pdus) > maximumSMSParts {
		return nil, fmt.Errorf("SMS requires %d parts; maximum is %d", len(pdus), maximumSMSParts)
	}
	if _, err := owner.Exchange(ctx, "AT+CMGF=0", 3*time.Second); err != nil {
		return nil, err
	}
	references := make([]int, 0, len(pdus))
	for _, message := range pdus {
		tp, marshalErr := message.MarshalBinary()
		if marshalErr != nil {
			return nil, marshalErr
		}
		pdu := pdumode.PDU{TPDU: tp}
		wire, marshalErr := pdu.MarshalHexString()
		if marshalErr != nil {
			return nil, marshalErr
		}
		response, possiblySent, sendErr := owner.submitSMSPDU(ctx, len(tp), wire)
		if sendErr != nil {
			return references, &SMSSubmitError{PossiblySent: possiblySent || len(references) > 0, Err: sendErr}
		}
		match := smsReference.FindSubmatch(response)
		if len(match) != 2 {
			return references, &SMSSubmitError{PossiblySent: true, Err: errors.New("SMS submit response omitted message reference")}
		}
		reference, parseErr := strconv.Atoi(string(match[1]))
		if parseErr != nil || reference < 0 || reference > 255 {
			return references, &SMSSubmitError{PossiblySent: true, Err: errors.New("SMS submit returned an invalid message reference")}
		}
		references = append(references, reference)
	}
	return references, nil
}

func (owner *Owner) submitSMSPDU(ctx context.Context, length int, payload string) ([]byte, bool, error) {
	if length < 1 || length > 140 || len(payload) < 2 || len(payload) > 1024 {
		return nil, false, errors.New("invalid SMS PDU")
	}
	if _, err := hex.DecodeString(payload); err != nil {
		return nil, false, errors.New("invalid SMS PDU encoding")
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.port == nil {
		return nil, false, errors.New("AT control port is closed")
	}
	if submitter, ok := owner.port.(interface {
		SubmitSMSPDU(context.Context, int, string) ([]byte, bool, error)
	}); ok {
		return submitter.SubmitSMSPDU(ctx, length, payload)
	}
	if err := owner.port.ResetInputBuffer(); err != nil {
		return nil, false, fmt.Errorf("reset AT input: %w", err)
	}
	command := fmt.Sprintf("AT+CMGS=%d\r", length)
	if _, err := owner.port.Write([]byte(command)); err != nil {
		return nil, false, fmt.Errorf("write SMS command: %w", err)
	}
	if err := owner.port.Drain(); err != nil {
		return nil, false, fmt.Errorf("drain SMS command: %w", err)
	}
	prompt, err := owner.readSMSResponse(ctx, 8*time.Second, true)
	if err != nil {
		return nil, false, err
	}
	if !strings.Contains(string(prompt), ">") {
		return nil, false, errors.New("SMS modem did not return a submit prompt")
	}
	if _, err := owner.port.Write(append([]byte(payload), 0x1a)); err != nil {
		return nil, true, fmt.Errorf("write SMS PDU: %w", err)
	}
	if err := owner.port.Drain(); err != nil {
		return nil, true, fmt.Errorf("drain SMS PDU: %w", err)
	}
	response, err := owner.readSMSResponse(ctx, 120*time.Second, false)
	if err != nil {
		return nil, true, err
	}
	return response, false, nil
}

func (owner *Owner) readSMSResponse(ctx context.Context, timeout time.Duration, prompt bool) ([]byte, error) {
	deadline := time.Now().Add(timeout)
	response := make([]byte, 0, 512)
	buffer := make([]byte, 512)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		count, err := owner.port.Read(buffer)
		if err != nil {
			return nil, fmt.Errorf("read SMS response: %w", err)
		}
		if count == 0 {
			continue
		}
		response = append(response, buffer[:count]...)
		if len(response) > 32*1024 {
			return nil, errors.New("SMS response exceeded 32 KiB")
		}
		if prompt && strings.Contains(string(response), ">") {
			return response, nil
		}
		if terminal, accepted := terminalResponse(response); terminal {
			if accepted {
				return response, nil
			}
			return nil, fmt.Errorf("SMS command rejected: %s", boundedTail(response))
		}
	}
	return nil, errors.New("SMS command timed out")
}

type decodedSMS struct {
	index int
	state int
	wire  string
	tpdu  *tpdu.TPDU
}

type decodedSMSGroup struct {
	segments int
	parts    map[int]decodedSMS
}

func decodeSMSList(response []byte, fallback time.Time) ([]SMSMessage, error) {
	matches := smsListHeader.FindAllSubmatch(response, -1)
	decoded := make([]decodedSMS, 0, len(matches))
	for _, match := range matches {
		index, _ := strconv.Atoi(string(match[1]))
		state, _ := strconv.Atoi(string(match[2]))
		wire := string(match[3])
		pdu, err := pdumode.UnmarshalHexString(wire)
		if err != nil {
			return nil, fmt.Errorf("decode SMS PDU at index %d: %w", index, err)
		}
		direction := sms.AsMT
		if state == 2 || state == 3 {
			direction = sms.AsMO
		}
		message, err := sms.Unmarshal(pdu.TPDU, direction)
		if err != nil {
			return nil, fmt.Errorf("decode SMS TPDU at index %d: %w", index, err)
		}
		decoded = append(decoded, decodedSMS{index: index, state: state, wire: wire, tpdu: message})
	}
	result := make([]SMSMessage, 0, len(decoded))
	groups := map[string][]*decodedSMSGroup{}
	for _, source := range decoded {
		segments, sequence, reference, concatenated := source.tpdu.ConcatInfo()
		if !concatenated || segments < 2 {
			fact, supported, err := smsFact([]decodedSMS{source}, fallback)
			if err != nil {
				return nil, err
			}
			if supported {
				result = append(result, fact)
			}
			continue
		}
		if sequence < 1 || sequence > segments || segments > maximumSMSParts {
			continue
		}
		key := smsGroupKey(source.tpdu, segments, reference)
		var selected *decodedSMSGroup
		for _, candidate := range groups[key] {
			if candidate.parts[sequence].tpdu == nil {
				selected = candidate
				break
			}
		}
		if selected == nil {
			selected = &decodedSMSGroup{segments: segments, parts: map[int]decodedSMS{}}
			groups[key] = append(groups[key], selected)
		}
		selected.parts[sequence] = source
	}
	for _, candidates := range groups {
		for _, group := range candidates {
			if len(group.parts) != group.segments {
				continue
			}
			parts := make([]decodedSMS, group.segments)
			for sequence := 1; sequence <= group.segments; sequence++ {
				parts[sequence-1] = group.parts[sequence]
			}
			fact, supported, err := smsFact(parts, fallback)
			if err != nil {
				return nil, err
			}
			if supported {
				result = append(result, fact)
			}
		}
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Index < result[right].Index })
	return result, nil
}

func smsGroupKey(message *tpdu.TPDU, segments, reference int) string {
	peer := message.OA.Number()
	if message.SmsType() == tpdu.SmsSubmit {
		peer = message.DA.Number()
	}
	return fmt.Sprintf("%d:%s:%d:%d", message.SmsType(), peer, reference, segments)
}

func smsFact(sources []decodedSMS, fallback time.Time) (SMSMessage, bool, error) {
	if len(sources) == 0 {
		return SMSMessage{}, false, errors.New("empty SMS fact")
	}
	message := sources[0].tpdu
	wires := make([]string, len(sources))
	segments := make([]*tpdu.TPDU, len(sources))
	index := sources[0].index
	for position, source := range sources {
		wires[position], segments[position] = strings.ToUpper(source.wire), source.tpdu
		if source.index < index {
			index = source.index
		}
	}
	fingerprint := sha256.Sum256([]byte(strings.Join(wires, "\n")))
	fact := SMSMessage{Index: index, ObservedAt: fallback, Fingerprint: hex.EncodeToString(fingerprint[:])}
	switch message.SmsType() {
	case tpdu.SmsDeliver:
		body, err := sms.Decode(segments)
		if err != nil {
			return SMSMessage{}, false, err
		}
		fact.State, fact.Direction, fact.Peer, fact.Body = "received", "in", message.OA.Number(), string(body)
		if !message.SCTS.IsZero() {
			fact.ObservedAt = message.SCTS.UTC()
		}
	case tpdu.SmsSubmit:
		body, err := sms.Decode(segments)
		if err != nil {
			return SMSMessage{}, false, err
		}
		fact.State, fact.Direction, fact.Peer, fact.Body = "stored", "out", message.DA.Number(), string(body)
	case tpdu.SmsStatusReport:
		fact.State, fact.Direction, fact.Peer = "delivery", "in", message.RA.Number()
		fact.Reference = int(message.MR)
		fact.DeliveryState = deliveryState(message.ST)
		if !message.DT.IsZero() {
			fact.ObservedAt = message.DT.UTC()
		}
	default:
		return SMSMessage{}, false, nil
	}
	return fact, true, nil
}

func deliveryState(status byte) string {
	switch {
	case status <= 0x1f:
		return "delivered"
	case status <= 0x3f:
		return "temporary_failure"
	case status <= 0x7f:
		return "permanent_failure"
	default:
		return "unknown"
	}
}
