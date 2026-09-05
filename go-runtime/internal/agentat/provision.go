package agentat

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
)

var provisionDigits = regexp.MustCompile(`^[0-9]+$`)
var provisionSMSC = regexp.MustCompile(`^\+[0-9]{5,20}$`)

// ProvisionHardware applies only modem fields that have a portable, verifiable
// AT contract. IMEI and subscriber identity are observations, never writes.
type ProvisionHardware struct {
	AT ProvisionAT
}

type ProvisionAT interface {
	SIMPINStatusFresh(context.Context, string) (SIMPINStatus, error)
	Exchange(context.Context, string, string, time.Duration) ([]byte, error)
}

func NewProvisionHardware(at ProvisionAT) ProvisionHardware {
	return ProvisionHardware{AT: at}
}

// ReadProvision observes the exact modem/SIM state without issuing any write.
func (adapter ProvisionHardware) ReadProvision(ctx context.Context, request agentlink.ProvisionRequest) (string, agentlink.ProvisionReadback, error) {
	if adapter.AT == nil {
		return "at_manager", agentlink.ProvisionReadback{}, errors.New("AT manager unavailable")
	}
	status, err := adapter.AT.SIMPINStatusFresh(ctx, request.EquipmentID)
	if err != nil {
		return "sim_pin_status", agentlink.ProvisionReadback{}, err
	}
	if status.CardID != request.CardID || status.State != SIMPINNotRequired {
		return "sim_identity", agentlink.ProvisionReadback{}, errors.New("SIM identity or PIN state changed")
	}
	readback := agentlink.ProvisionReadback{
		EquipmentID: request.EquipmentID, CardID: status.CardID,
		SIMSessionGeneration: request.SIMSessionGeneration,
	}
	if err := adapter.readIdentity(ctx, request, &readback); err != nil {
		return "read_identity", agentlink.ProvisionReadback{}, err
	}
	return "readback", readback, nil
}

func (adapter ProvisionHardware) ApplyProvision(ctx context.Context, request agentlink.ProvisionRequest) (string, agentlink.ProvisionReadback, error) {
	if adapter.AT == nil {
		return "at_manager", agentlink.ProvisionReadback{}, errors.New("AT manager unavailable")
	}
	if request.IMEI == "" || !provisionDigits.MatchString(request.IMEI) {
		return "validate_identity", agentlink.ProvisionReadback{}, errors.New("invalid requested IMEI")
	}
	if request.IMSI == "" || !provisionDigits.MatchString(request.IMSI) {
		return "validate_identity", agentlink.ProvisionReadback{}, errors.New("invalid requested IMSI")
	}
	status, err := adapter.AT.SIMPINStatusFresh(ctx, request.EquipmentID)
	if err != nil {
		return "sim_pin_status", agentlink.ProvisionReadback{}, err
	}
	if status.CardID != request.CardID || status.State != SIMPINNotRequired {
		return "sim_identity", agentlink.ProvisionReadback{}, fmt.Errorf("SIM identity or PIN state changed")
	}

	readback := agentlink.ProvisionReadback{
		EquipmentID: request.EquipmentID, CardID: status.CardID,
		SIMSessionGeneration: request.SIMSessionGeneration,
	}
	if err := adapter.readIdentity(ctx, request, &readback); err != nil {
		return "read_identity", agentlink.ProvisionReadback{}, err
	}
	if request.SMSC != "" {
		if !provisionSMSC.MatchString(request.SMSC) {
			return "validate_smsc", agentlink.ProvisionReadback{}, errors.New("invalid SMSC")
		}
		if _, err := adapter.AT.Exchange(ctx, request.EquipmentID, `AT+CSCA="`+request.SMSC+`"`, 5*time.Second); err != nil {
			return "write_smsc", agentlink.ProvisionReadback{}, err
		}
	}
	if request.APN != "" {
		if strings.ContainsAny(request.APN, "\"\r\n") {
			return "validate_apn", agentlink.ProvisionReadback{}, errors.New("invalid APN")
		}
		if _, err := adapter.AT.Exchange(ctx, request.EquipmentID, `AT+CGDCONT=1,"IP","`+request.APN+`"`, 5*time.Second); err != nil {
			return "write_apn", agentlink.ProvisionReadback{}, err
		}
	}
	if err := adapter.readIdentity(ctx, request, &readback); err != nil {
		return "readback", agentlink.ProvisionReadback{}, err
	}
	if request.SMSC != "" && readback.SMSC != request.SMSC {
		return "readback_smsc", agentlink.ProvisionReadback{}, errors.New("SMSC readback mismatch")
	}
	if request.APN != "" && readback.APN != request.APN {
		return "readback_apn", agentlink.ProvisionReadback{}, errors.New("APN readback mismatch")
	}
	return "readback", readback, nil
}

func (adapter ProvisionHardware) readIdentity(ctx context.Context, request agentlink.ProvisionRequest, readback *agentlink.ProvisionReadback) error {
	imei, err := adapter.AT.Exchange(ctx, request.EquipmentID, "AT+CGSN", 3*time.Second)
	if err != nil || firstDigits(imei, 15) != request.IMEI {
		return errors.New("IMEI readback mismatch")
	}
	imsi, err := adapter.AT.Exchange(ctx, request.EquipmentID, "AT+CIMI", 3*time.Second)
	if err != nil || firstDigits(imsi, len(request.IMSI)) != request.IMSI {
		return errors.New("IMSI readback mismatch")
	}
	smsc, err := adapter.AT.Exchange(ctx, request.EquipmentID, "AT+CSCA?", 3*time.Second)
	if err != nil {
		return err
	}
	operator, err := adapter.AT.Exchange(ctx, request.EquipmentID, "AT+COPS?", 3*time.Second)
	if err != nil {
		return err
	}
	readback.IMSI, readback.IMEI = request.IMSI, request.IMEI
	readback.MCC, readback.MNC = parseProvisionPLMN(operator)
	readback.SMSC = parseProvisionSMSC(smsc)
	if request.IMEISV != "" {
		imeisv, err := adapter.AT.Exchange(ctx, request.EquipmentID, "AT+CGSN=1", 3*time.Second)
		if err != nil {
			return err
		}
		readback.IMEISV = firstDigits(imeisv, 16)
	}
	if request.MSISDN != "" {
		number, err := adapter.AT.Exchange(ctx, request.EquipmentID, "AT+CNUM", 3*time.Second)
		if err != nil {
			return err
		}
		readback.MSISDN = parseProvisionMSISDN(number)
	}
	apn, err := adapter.AT.Exchange(ctx, request.EquipmentID, "AT+CGDCONT?", 3*time.Second)
	if err != nil {
		return err
	}
	readback.APN = parseProvisionAPN(apn)
	return nil
}

func parseProvisionPLMN(value []byte) (string, string) {
	for _, field := range strings.FieldsFunc(string(value), func(r rune) bool {
		return r == '"' || r == ',' || r == '\r' || r == '\n' || r == ' '
	}) {
		if len(field) != 5 && len(field) != 6 || !provisionDigits.MatchString(field) {
			continue
		}
		return field[:3], field[3:]
	}
	return "", ""
}

func parseProvisionMSISDN(value []byte) string {
	for _, field := range strings.FieldsFunc(string(value), func(r rune) bool {
		return r == '"' || r == ',' || r == '\r' || r == '\n' || r == ' '
	}) {
		if strings.HasPrefix(field, "+") && len(field) >= 6 && provisionDigits.MatchString(field[1:]) {
			return field
		}
	}
	return ""
}

func firstDigits(value []byte, length int) string {
	for _, field := range strings.Fields(string(value)) {
		if len(field) >= length && provisionDigits.MatchString(field) {
			return field[:length]
		}
	}
	return ""
}

func parseProvisionSMSC(value []byte) string {
	start := strings.IndexByte(string(value), '"')
	if start < 0 {
		return ""
	}
	rest := string(value)[start+1:]
	end := strings.IndexByte(rest, '"')
	if end < 0 {
		return ""
	}
	return rest[:end]
}

func parseProvisionAPN(value []byte) string {
	fields := strings.Split(string(value), ",")
	if len(fields) < 3 {
		return ""
	}
	return strings.Trim(fields[2], " \t\r\n\"")
}
