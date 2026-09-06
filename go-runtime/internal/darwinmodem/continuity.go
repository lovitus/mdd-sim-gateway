package darwinmodem

import "strings"

func continuityFailureCode(err error) string {
	detail := strings.ToLower(err.Error())
	switch {
	case strings.Contains(detail, "isolation_not_proven"):
		return "isolation_check_failed"
	case strings.Contains(detail, "sim pin state"):
		return "sim_pin_state_failed"
	case strings.Contains(detail, "sim identity"), strings.Contains(detail, "sim iccid"):
		return "sim_card_identity_failed"
	default:
		return "modem_identity_probe_failed"
	}
}
