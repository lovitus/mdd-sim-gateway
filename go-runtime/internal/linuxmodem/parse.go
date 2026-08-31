package linuxmodem

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
)

var (
	imsiLine        = regexp.MustCompile(`(?m)^\s*([0-9]{5,16})\s*$`)
	cnumLine        = regexp.MustCompile(`(?mi)^\s*\+CNUM:[^\r\n]*?"([+0-9]{1,64})"`)
	registration    = regexp.MustCompile(`(?mi)^\s*\+(?:CEREG|CGREG|CREG):\s*(?:[0-9]+\s*,\s*)?([0-9]+)`)
	operatorLine    = regexp.MustCompile(`(?mi)^\s*\+COPS:\s*[^,]*\s*,\s*[^,]*\s*,\s*"([^"]*)"`)
	operatorNumeric = regexp.MustCompile(`^[0-9]{5,6}$`)
	csqLine         = regexp.MustCompile(`(?mi)^\s*\+CSQ:\s*([0-9]+)\s*,`)
	cfunLine        = regexp.MustCompile(`(?mi)^\s*\+CFUN:\s*([0-9]+)`)
	cscaLine        = regexp.MustCompile(`(?mi)^\s*\+CSCA:\s*"([^"]*)"`)
)

func parseIMSI(value []byte) string {
	for _, match := range imsiLine.FindAllSubmatch(value, -1) {
		candidate := string(match[1])
		if len(candidate) >= 14 {
			return candidate
		}
	}
	return ""
}

func parseMSISDNs(value []byte) []string {
	seen := map[string]struct{}{}
	result := make([]string, 0)
	for _, match := range cnumLine.FindAllSubmatch(value, -1) {
		candidate := strings.TrimSpace(string(match[1]))
		if _, exists := seen[candidate]; candidate != "" && !exists {
			seen[candidate] = struct{}{}
			result = append(result, candidate)
		}
	}
	return result
}

func parseRegistration(value []byte) agentmodem.RegistrationState {
	match := registration.FindSubmatch(value)
	if len(match) != 2 {
		return agentmodem.RegistrationUnknown
	}
	state, _ := strconv.Atoi(string(match[1]))
	switch state {
	case 0:
		return agentmodem.RegistrationUnregistered
	case 1:
		return agentmodem.RegistrationHome
	case 2:
		return agentmodem.RegistrationSearching
	case 3:
		return agentmodem.RegistrationDenied
	case 5:
		return agentmodem.RegistrationRoaming
	default:
		return agentmodem.RegistrationUnknown
	}
}

func parseOperator(value []byte) (id, name string) {
	match := operatorLine.FindSubmatch(value)
	if len(match) != 2 {
		return "", ""
	}
	candidate := strings.TrimSpace(string(match[1]))
	if operatorNumeric.MatchString(candidate) {
		return candidate, ""
	}
	return "", candidate
}

func parseSignal(value []byte) *uint32 {
	match := csqLine.FindSubmatch(value)
	if len(match) != 2 {
		return nil
	}
	rssi, err := strconv.Atoi(string(match[1]))
	if err != nil || rssi < 0 || rssi == 99 {
		return nil
	}
	if rssi > 31 {
		rssi = 31
	}
	percent := uint32((rssi*100 + 15) / 31)
	return &percent
}

func parseRadio(value []byte) agentmodem.RadioState {
	match := cfunLine.FindSubmatch(value)
	if len(match) != 2 {
		return agentmodem.RadioUnknown
	}
	if string(match[1]) == "1" {
		return agentmodem.RadioOn
	}
	return agentmodem.RadioOff
}

func parseSMSC(value []byte) string {
	match := cscaLine.FindSubmatch(value)
	if len(match) == 2 {
		return strings.TrimSpace(string(match[1]))
	}
	return ""
}

func bounded(value string, maximum int) string {
	value = strings.TrimSpace(strings.ToValidUTF8(value, "?"))
	if len(value) > maximum {
		value = strings.ToValidUTF8(value[:maximum], "?")
	}
	return value
}
