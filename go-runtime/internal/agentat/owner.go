// Package agentat owns one auxiliary 3GPP AT function. Discovery and every
// later operation share the same lock and open handle, so two components never
// race the modem control port.
package agentat

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type Port interface {
	io.ReadWriteCloser
	Drain() error
	ResetInputBuffer() error
}

type Candidate struct {
	Name    string
	Product string
	USB     bool
	// PhysicalID is the passive OS parent-devnode identity shared by all
	// functions of one composite modem. It is never inferred from a label.
	PhysicalID string
}

type Opener func(Candidate) (Port, error)

type Capabilities struct {
	CallSignalling bool
	SMS            bool
	SIMAPDU        bool
	VoicePCM       bool
}

type Owner struct {
	mu           sync.Mutex
	port         Port
	candidate    Candidate
	equipmentID  string
	simAPDU      bool
	capabilities Capabilities
}

type DiscoveryError struct {
	Candidates           int
	Busy                 bool
	LastBusyPort         string
	Opened               int
	OpenFailures         int
	LastOpenFailurePort  string
	LastOpenError        string
	Mismatched           int
	LastMismatchPort     string
	ProbeFailures        int
	LastProbeFailurePort string
}

func (err DiscoveryError) Error() string {
	switch {
	case err.Candidates == 0:
		return "no safe AT control port candidates were enumerated"
	case err.Busy:
		return fmt.Sprintf(
			"AT control discovery found %d candidate(s); %s is already owned, %d opened, %d identity mismatch(es), %d probe failure(s)",
			err.Candidates, safePort(err.LastBusyPort), err.Opened, err.Mismatched, err.ProbeFailures)
	case err.OpenFailures > 0 && err.Opened == 0:
		return fmt.Sprintf(
			"AT control discovery could not open %s: %s",
			safePort(err.LastOpenFailurePort), boundedTail([]byte(err.LastOpenError)))
	case err.Mismatched > 0:
		return fmt.Sprintf(
			"AT control discovery opened %d candidate(s); %s was the last of %d identity response(s) that did not match the MBN equipment identity",
			err.Opened, safePort(err.LastMismatchPort), err.Mismatched)
	case err.ProbeFailures > 0:
		return fmt.Sprintf(
			"AT control discovery opened %d candidate(s); %s was the last of %d that did not answer the read-only identity probe",
			err.Opened, safePort(err.LastProbeFailurePort), err.ProbeFailures)
	}
	return "no matching auxiliary AT control port was found"
}

var (
	equipmentIDPattern = regexp.MustCompile(`^[0-9]{14,17}$`)
	numericIdentity    = regexp.MustCompile(`[0-9]{14,17}`)
)

// Discover opens candidates in deterministic order and retains the first port
// whose fresh AT+CGSN identity exactly matches equipmentID. All non-matching
// handles are closed before the next candidate is attempted.
func Discover(ctx context.Context, equipmentID string, candidates []Candidate, open Opener) (*Owner, error) {
	return discover(ctx, equipmentID, candidates, open, false, false)
}

// DiscoverWithSIMAPDU retains the same exact-equipment ownership proof while
// optionally probing the typed CCHO/CGLA/CCHC AKA transport. It never exposes
// a generic raw-APDU endpoint to callers.
func DiscoverWithSIMAPDU(ctx context.Context, equipmentID string, candidates []Candidate, open Opener, simAPDU bool) (*Owner, error) {
	return discover(ctx, equipmentID, candidates, open, simAPDU, simAPDU)
}

// DiscoverWithDeferredSIMAPDU proves and retains the auxiliary AT owner but
// leaves UICC test commands for an explicit data-off operation.
func DiscoverWithDeferredSIMAPDU(ctx context.Context, equipmentID string, candidates []Candidate, open Opener, simAPDU bool) (*Owner, error) {
	return discover(ctx, equipmentID, candidates, open, simAPDU, false)
}

func discover(ctx context.Context, equipmentID string, candidates []Candidate, open Opener, simAPDU, probeSIMAPDU bool) (*Owner, error) {
	if !equipmentIDPattern.MatchString(equipmentID) || open == nil {
		return nil, errors.New("invalid AT discovery request")
	}
	candidates = normalizedCandidates(candidates)
	discovery := DiscoveryError{Candidates: len(candidates)}
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		port, err := open(candidate)
		if err != nil {
			if IsBusy(err) {
				discovery.Busy = true
				discovery.LastBusyPort = candidate.Name
			} else {
				discovery.OpenFailures++
				discovery.LastOpenFailurePort = candidate.Name
				discovery.LastOpenError = err.Error()
			}
			continue
		}
		discovery.Opened++
		owner := &Owner{port: port, candidate: candidate, equipmentID: equipmentID, simAPDU: simAPDU}
		matched, probeErr := owner.identify(ctx)
		if probeErr != nil || !matched {
			_ = owner.Close()
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if probeErr != nil {
				discovery.ProbeFailures++
				discovery.LastProbeFailurePort = candidate.Name
			} else {
				discovery.Mismatched++
				discovery.LastMismatchPort = candidate.Name
			}
			continue
		}
		owner.capabilities = owner.probeCapabilities(ctx, probeSIMAPDU)
		if err := ctx.Err(); err != nil {
			_ = owner.Close()
			return nil, err
		}
		return owner, nil
	}
	return nil, discovery
}

type busyError interface {
	Busy() bool
}

func IsBusy(err error) bool {
	var value busyError
	return errors.As(err, &value) && value.Busy()
}

func (owner *Owner) Name() string { return owner.candidate.Name }

func (owner *Owner) EquipmentID() string { return owner.equipmentID }

func (owner *Owner) PhysicalID() string { return owner.candidate.PhysicalID }

func (owner *Owner) Capabilities() Capabilities { return owner.capabilities }

func (owner *Owner) Healthy(ctx context.Context) error {
	_, err := owner.Exchange(ctx, "AT", 2*time.Second)
	return err
}

func (owner *Owner) identify(ctx context.Context) (bool, error) {
	if _, err := owner.Exchange(ctx, "AT", 2*time.Second); err != nil {
		return false, err
	}
	response, err := owner.Exchange(ctx, "AT+CGSN", 3*time.Second)
	if err != nil {
		return false, err
	}
	for _, value := range numericIdentity.FindAllString(string(response), -1) {
		if value == owner.equipmentID {
			return true, nil
		}
	}
	return false, nil
}

func (owner *Owner) probeCapabilities(ctx context.Context, probeSIMAPDU bool) Capabilities {
	result := Capabilities{}
	if _, err := owner.Exchange(ctx, "AT+CLCC", 3*time.Second); err == nil {
		result.CallSignalling = true
	}
	if _, err := owner.Exchange(ctx, "AT+CMGF=?", 3*time.Second); err == nil {
		result.SMS = true
	}
	if response, err := owner.Exchange(ctx, "AT+QPCMV=?", 3*time.Second); err == nil && supportsVoicePCM(response) {
		result.VoicePCM = true
	}
	if owner.simAPDU && probeSIMAPDU {
		ready, _ := owner.PrepareSIMAPDU(ctx)
		result.SIMAPDU = ready
	}
	return result
}

func (owner *Owner) PrepareSIMAPDU(ctx context.Context) (bool, error) {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if !owner.simAPDU {
		return false, errors.New("SIM APDU is disabled for this AT owner")
	}
	if owner.capabilities.SIMAPDU {
		return true, nil
	}
	// Only standardized test forms run here. A real logical channel remains
	// scoped to one typed AKA request and is always closed by that operation.
	for _, command := range []string{"AT+CCHO=?", "AT+CGLA=?", "AT+CCHC=?"} {
		if _, err := owner.exchangeLocked(ctx, command, 3*time.Second); err != nil {
			return false, err
		}
	}
	owner.capabilities.SIMAPDU = true
	return true, nil
}

func (owner *Owner) Exchange(ctx context.Context, command string, timeout time.Duration) ([]byte, error) {
	if err := validateCommand(command); err != nil {
		return nil, err
	}
	if timeout <= 0 {
		return nil, errors.New("AT command timeout must be positive")
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	return owner.exchangeLocked(ctx, command, timeout)
}

func (owner *Owner) exchangeLocked(ctx context.Context, command string, timeout time.Duration) ([]byte, error) {
	if owner.port == nil {
		return nil, errors.New("AT control port is closed")
	}
	if transaction, ok := owner.port.(interface {
		Exchange(context.Context, string, time.Duration) ([]byte, error)
	}); ok {
		return transaction.Exchange(ctx, command, timeout)
	}
	if err := owner.port.ResetInputBuffer(); err != nil {
		return nil, fmt.Errorf("reset AT input: %w", err)
	}
	if _, err := owner.port.Write([]byte(command + "\r")); err != nil {
		return nil, fmt.Errorf("write AT command: %w", err)
	}
	if err := owner.port.Drain(); err != nil {
		return nil, fmt.Errorf("drain AT command: %w", err)
	}
	deadline := time.Now().Add(timeout)
	response := make([]byte, 0, 512)
	buffer := make([]byte, 512)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		count, err := owner.port.Read(buffer)
		if err != nil {
			return nil, fmt.Errorf("read AT response: %w", err)
		}
		if count > 0 {
			response = append(response, buffer[:count]...)
			if len(response) > 32*1024 {
				return nil, errors.New("AT response exceeded 32 KiB")
			}
			terminal, accepted := terminalResponse(response)
			if terminal {
				if accepted {
					return response, nil
				}
				return nil, fmt.Errorf("AT command rejected: %s", boundedTail(response))
			}
		}
	}
	return nil, errors.New("AT command timed out")
}

func (owner *Owner) Close() error {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.port == nil {
		return nil
	}
	port := owner.port
	owner.port = nil
	return port.Close()
}

func validateCommand(value string) error {
	if len(value) < 2 || len(value) > 128 || !strings.HasPrefix(value, "AT") {
		return errors.New("invalid AT command")
	}
	for _, character := range value {
		if character < 0x20 || character > 0x7e {
			return errors.New("invalid AT command")
		}
	}
	return nil
}

func terminalResponse(value []byte) (terminal bool, accepted bool) {
	lines := strings.FieldsFunc(string(value), func(character rune) bool {
		return character == '\r' || character == '\n'
	})
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "OK" {
			return true, true
		}
		if line == "ERROR" || strings.HasPrefix(line, "+CME ERROR:") || strings.HasPrefix(line, "+CMS ERROR:") {
			return true, false
		}
	}
	return false, false
}

func boundedTail(value []byte) string {
	if len(value) > 200 {
		value = value[len(value)-200:]
	}
	return strings.TrimSpace(strings.ToValidUTF8(string(value), "?"))
}

func safePort(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 64 {
		return "a candidate port"
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || strings.ContainsRune("-_.:", character) {
			continue
		}
		return "a candidate port"
	}
	return value
}

func normalizedCandidates(input []Candidate) []Candidate {
	seen := make(map[string]struct{}, len(input))
	result := make([]Candidate, 0, len(input))
	for _, candidate := range input {
		candidate.Name = strings.TrimSpace(candidate.Name)
		candidate.Product = strings.TrimSpace(candidate.Product)
		if candidate.Name == "" || unsafeProduct(candidate.Product) {
			continue
		}
		key := strings.ToLower(candidate.Name)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, candidate)
	}
	sort.Slice(result, func(left, right int) bool {
		leftScore, rightScore := candidateScore(result[left]), candidateScore(result[right])
		if leftScore != rightScore {
			return leftScore < rightScore
		}
		return strings.ToLower(result[left].Name) < strings.ToLower(result[right].Name)
	})
	return result
}

func unsafeProduct(value string) bool {
	value = strings.ToLower(value)
	for _, marker := range []string{"bluetooth", "nmea", "diagnostic", " diag", "gps"} {
		if strings.Contains(value, marker) {
			return true
		}
	}
	for _, word := range strings.FieldsFunc(value, func(character rune) bool { return !isAlphaNumeric(character) }) {
		if word == "dm" || word == "diag" {
			return true
		}
	}
	return false
}

func candidateScore(value Candidate) int {
	product := strings.ToLower(value.Product)
	if strings.Contains(product, "modem") {
		return 0
	}
	for _, word := range strings.FieldsFunc(product, func(character rune) bool { return !isAlphaNumeric(character) }) {
		if word == "at" {
			return 1
		}
	}
	if value.USB {
		return 2
	}
	return 3
}

func isAlphaNumeric(character rune) bool {
	return character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
		character >= '0' && character <= '9'
}
