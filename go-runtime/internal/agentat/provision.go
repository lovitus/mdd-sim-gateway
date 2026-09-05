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

type provisionFailure struct {
	code string
	err  error
}

func (failure provisionFailure) Error() string                { return failure.err.Error() }
func (failure provisionFailure) Unwrap() error                { return failure.err }
func (failure provisionFailure) ProvisionFailureCode() string { return failure.code }

func provisionFailed(code string, err error) error {
	return provisionFailure{code: code, err: err}
}

type ProvisionAT interface {
	SIMPINStatusFresh(context.Context, string) (SIMPINStatus, error)
	Exchange(context.Context, string, string, time.Duration) ([]byte, error)
}

type provisionATTransaction interface {
	WithProvisionTransaction(context.Context, func(ProvisionAT) error) error
}

func NewProvisionHardware(at ProvisionAT) ProvisionHardware {
	return ProvisionHardware{AT: at}
}

// ReadProvision observes the exact modem/SIM state without issuing any write.
func (adapter ProvisionHardware) ReadProvision(ctx context.Context, request agentlink.ProvisionRequest) (string, agentlink.ProvisionReadback, error) {
	var step string
	var readback agentlink.ProvisionReadback
	err := adapter.withTransaction(ctx, func(at ProvisionAT) error {
		var readErr error
		step, readback, readErr = (ProvisionHardware{AT: at}).readProvision(ctx, request)
		return readErr
	})
	return step, readback, err
}

func (adapter ProvisionHardware) readProvision(ctx context.Context, request agentlink.ProvisionRequest) (string, agentlink.ProvisionReadback, error) {
	if adapter.AT == nil {
		return "at_manager", agentlink.ProvisionReadback{}, errors.New("AT manager unavailable")
	}
	status, err := adapter.AT.SIMPINStatusFresh(ctx, request.EquipmentID)
	if err != nil {
		return "sim_pin_status", agentlink.ProvisionReadback{}, provisionFailed("provision_pin_status_unavailable", err)
	}
	if status.CardID != request.CardID || status.State != SIMPINNotRequired {
		return "sim_identity", agentlink.ProvisionReadback{}, provisionFailed("provision_sim_identity_mismatch", errors.New("SIM identity or PIN state changed"))
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
	var step string
	var readback agentlink.ProvisionReadback
	err := adapter.withTransaction(ctx, func(at ProvisionAT) error {
		var applyErr error
		step, readback, applyErr = (ProvisionHardware{AT: at}).applyProvision(ctx, request)
		return applyErr
	})
	return step, readback, err
}

func (adapter ProvisionHardware) applyProvision(ctx context.Context, request agentlink.ProvisionRequest) (string, agentlink.ProvisionReadback, error) {
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
		return "sim_pin_status", agentlink.ProvisionReadback{}, provisionFailed("provision_pin_status_unavailable", err)
	}
	if status.CardID != request.CardID || status.State != SIMPINNotRequired {
		return "sim_identity", agentlink.ProvisionReadback{}, provisionFailed("provision_sim_identity_mismatch", fmt.Errorf("SIM identity or PIN state changed"))
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
			return "write_smsc", agentlink.ProvisionReadback{}, provisionFailed("provision_smsc_write_failed", err)
		}
	}
	if request.APN != "" {
		if strings.ContainsAny(request.APN, "\"\r\n") {
			return "validate_apn", agentlink.ProvisionReadback{}, errors.New("invalid APN")
		}
		if _, err := adapter.AT.Exchange(ctx, request.EquipmentID, `AT+CGDCONT=1,"IP","`+request.APN+`"`, 5*time.Second); err != nil {
			return "write_apn", agentlink.ProvisionReadback{}, provisionFailed("provision_apn_write_failed", err)
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

func (adapter ProvisionHardware) withTransaction(ctx context.Context, callback func(ProvisionAT) error) error {
	if transaction, ok := adapter.AT.(provisionATTransaction); ok {
		return transaction.WithProvisionTransaction(ctx, callback)
	}
	return callback(adapter.AT)
}

func (adapter ProvisionHardware) readIdentity(ctx context.Context, request agentlink.ProvisionRequest, readback *agentlink.ProvisionReadback) error {
	imei, err := adapter.AT.Exchange(ctx, request.EquipmentID, "AT+CGSN", 3*time.Second)
	if err != nil || firstDigits(imei, 15) != request.IMEI {
		return provisionFailed("provision_imei_readback_failed", errors.New("IMEI readback mismatch"))
	}
	imsi, err := adapter.AT.Exchange(ctx, request.EquipmentID, "AT+CIMI", 3*time.Second)
	if err != nil || firstDigits(imsi, len(request.IMSI)) != request.IMSI {
		return provisionFailed("provision_imsi_readback_failed", errors.New("IMSI readback mismatch"))
	}
	smsc, err := adapter.AT.Exchange(ctx, request.EquipmentID, "AT+CSCA?", 3*time.Second)
	if err != nil {
		return provisionFailed("provision_smsc_readback_failed", err)
	}
	readback.IMSI, readback.IMEI = request.IMSI, request.IMEI
	homePLMN := request.MCC + request.MNC
	if homePLMN == "" || !strings.HasPrefix(readback.IMSI, homePLMN) {
		return provisionFailed("provision_plmn_readback_failed", errors.New("IMSI home PLMN readback mismatch"))
	}
	readback.MCC, readback.MNC = request.MCC, request.MNC
	readback.SMSC = parseProvisionSMSC(smsc)
	if request.IMEISV != "" {
		imeisv, err := adapter.AT.Exchange(ctx, request.EquipmentID, "AT+CGSN=1", 3*time.Second)
		if err != nil {
			return provisionFailed("provision_imeisv_readback_failed", err)
		}
		readback.IMEISV = firstDigits(imeisv, 16)
	}
	if request.MSISDN != "" {
		number, err := adapter.AT.Exchange(ctx, request.EquipmentID, "AT+CNUM", 3*time.Second)
		if err != nil {
			return provisionFailed("provision_msisdn_readback_failed", err)
		}
		readback.MSISDN = parseProvisionMSISDN(number)
	}
	apn, err := adapter.AT.Exchange(ctx, request.EquipmentID, "AT+CGDCONT?", 3*time.Second)
	if err != nil {
		return provisionFailed("provision_apn_readback_failed", err)
	}
	readback.APN = parseProvisionAPN(apn)
	return nil
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
