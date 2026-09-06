//go:build darwin

package darwinmodem

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentat"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentdata"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/cellulario"
)

var simAbsentCME = regexp.MustCompile(`(?i)\+CME ERROR:\s*10(?:\D|$)`)

type Prober struct {
	mu          sync.Mutex
	helper      string
	audioHelper string
	simAPDU     bool
	devices     map[string]*device
	retries     map[string]retryState
	sessions    *agentmodem.SIMInsertionTracker
	now         func() time.Time
}

type device struct {
	attachment          cellulario.Attachment
	client              *cellulario.Client
	owner               *agentat.Owner
	manufacturer        string
	model               string
	firmware            string
	lastQualified       time.Time
	lastFact            agentmodem.Fact
	lastFactAt          time.Time
	lastContinuityIssue string
}

type retryState struct {
	attempt int
	next    time.Time
	detail  string
}

func NewProber(simAPDU bool) (*Prober, error) {
	helper, err := cellulario.ResolveSibling("mdd-cellular-io")
	if err != nil {
		return nil, err
	}
	audioHelper, err := cellulario.ResolveSibling("mdd-call-audio-helper")
	if err != nil {
		return nil, err
	}
	sessions, err := agentmodem.NewSIMInsertionTracker()
	if err != nil {
		return nil, fmt.Errorf("initialize modem SIM insertion tracker: %w", err)
	}
	return &Prober{
		helper: helper, audioHelper: audioHelper, simAPDU: simAPDU,
		devices: make(map[string]*device), retries: make(map[string]retryState), sessions: sessions, now: time.Now,
	}, nil
}

func (prober *Prober) Probe(ctx context.Context) ([]agentmodem.Fact, error) {
	prober.mu.Lock()
	defer prober.mu.Unlock()
	return prober.probeLocked(ctx, false)
}

func (prober *Prober) ProbeSIMPINStatus(ctx context.Context) ([]agentmodem.Fact, error) {
	prober.mu.Lock()
	defer prober.mu.Unlock()
	facts, err := prober.probeLocked(ctx, true)
	if err != nil {
		return nil, err
	}
	for index := range facts {
		current := prober.find(facts[index].AttachmentID, facts[index].EquipmentID)
		if current == nil || facts[index].AT.State != agentmodem.ATControlReady {
			continue
		}
		status, statusErr := current.owner.SIMPINStatusFull(ctx)
		if statusErr != nil {
			facts[index].SIM.PINRecovery = "status_unavailable"
			continue
		}
		facts[index].SIM.ICCID = status.CardID
		facts[index].SIM.PINState = string(status.State)
		facts[index].SIM.PINAttempts = cloneUint32(status.AttemptsRemaining)
	}
	return facts, nil
}

func (prober *Prober) probeLocked(ctx context.Context, fresh bool) (facts []agentmodem.Fact, err error) {
	defer func() {
		if err != nil {
			prober.sessions.Invalidate()
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	prober.pruneDisconnected()
	claimed := make([]cellulario.Attachment, 0, len(prober.devices))
	for _, current := range prober.devices {
		claimed = append(claimed, current.attachment)
	}
	discoveryContext, cancel := context.WithTimeout(ctx, 30*time.Second)
	attachments, err := cellulario.Discover(discoveryContext, prober.helper, claimed)
	cancel()
	if err != nil {
		return nil, err
	}
	now := prober.now()
	present := make(map[string]struct{}, len(attachments)+len(prober.devices))
	for generation := range prober.devices {
		present[generation] = struct{}{}
	}
	for _, attachment := range attachments {
		generation := attachment.Generation()
		present[generation] = struct{}{}
		if prober.devices[generation] != nil {
			continue
		}
		if retry := prober.retries[generation]; retry.next.After(now) {
			continue
		}
		current, launchErr := prober.openDevice(ctx, attachment)
		if launchErr != nil {
			prober.recordRetry(generation, launchErr)
			continue
		}
		delete(prober.retries, generation)
		prober.devices[generation] = current
	}
	for generation := range prober.retries {
		if _, exists := present[generation]; !exists {
			delete(prober.retries, generation)
		}
	}

	facts = make([]agentmodem.Fact, 0, len(prober.devices)+len(prober.retries))
	for generation, current := range prober.devices {
		fact, refreshErr := prober.fact(ctx, current, fresh)
		if refreshErr != nil {
			if !current.client.Alive() {
				_ = current.owner.Close()
				delete(prober.devices, generation)
				prober.recordRetry(generation, refreshErr)
				continue
			}
			// fact() errors are qualification, AT-owner, SIM-state, or ICCID
			// failures. Keep the degraded device visible, but do not certify
			// continuity from a cached identity.
			fact.ContinuityEpoch = ""
			current.lastContinuityIssue = continuityFailureCode(refreshErr)
			fact.LastContinuityIssue = current.lastContinuityIssue
			fact.Condition = agentmodem.DeviceDegraded
			fact.Detail = bounded(refreshErr.Error(), 1024)
		}
		facts = append(facts, fact)
	}
	for generation, retry := range prober.retries {
		for _, attachment := range attachments {
			if attachment.Generation() == generation {
				facts = append(facts, unavailableFact(attachment, retry.detail))
				break
			}
		}
	}
	sort.Slice(facts, func(left, right int) bool { return facts[left].AttachmentID < facts[right].AttachmentID })
	return prober.sessions.Observe(facts), nil
}

func (prober *Prober) openDevice(ctx context.Context, attachment cellulario.Attachment) (*device, error) {
	client, err := cellulario.Launch(ctx, prober.helper, attachment)
	if err != nil {
		return nil, err
	}
	failed := true
	defer func() {
		if failed {
			_ = client.Close()
		}
	}()
	if err := client.Qualify(ctx); err != nil {
		return nil, fmt.Errorf("isolation_not_proven: %w", err)
	}
	identity, err := client.AT(ctx, "AT+CGSN", 3*time.Second)
	if err != nil {
		return nil, err
	}
	equipmentID := parseEquipmentID(identity)
	if equipmentID == "" {
		return nil, errors.New("raw USB modem did not return a valid equipment identity")
	}
	port := &helperPort{client: client}
	owner, err := agentat.DiscoverWithSIMAPDU(ctx, equipmentID, []agentat.Candidate{{
		Name: attachment.Generation(), Product: "raw USB modem", USB: true, PhysicalID: attachment.PhysicalID(),
	}}, func(agentat.Candidate) (agentat.Port, error) { return port, nil }, prober.simAPDU)
	if err != nil {
		return nil, err
	}
	current := &device{attachment: attachment, client: client, owner: owner, lastQualified: prober.now()}
	current.manufacturer = optionalInformation(ctx, owner, "AT+CGMI")
	current.model = optionalInformation(ctx, owner, "AT+CGMM")
	current.firmware = optionalInformation(ctx, owner, "AT+CGMR")
	failed = false
	return current, nil
}

func (prober *Prober) fact(ctx context.Context, current *device, fresh bool) (agentmodem.Fact, error) {
	now := prober.now()
	if now.Sub(current.lastQualified) >= 10*time.Second {
		if err := current.client.Qualify(ctx); err != nil {
			fact := cloneFact(current.lastFact)
			if fact.AttachmentID == "" {
				fact = unavailableFact(current.attachment, err.Error())
				fact.EquipmentID = current.owner.EquipmentID()
			}
			fact.Condition = agentmodem.DeviceDegraded
			fact.Network.Guard = agentmodem.DataGuardFact{State: agentmodem.DataGuardFailed, Detail: bounded(err.Error(), 1024)}
			return fact, fmt.Errorf("isolation_not_proven: %w", err)
		}
		current.lastQualified = now
	}
	// The isolated C companion deliberately has one serialized transaction lane.
	// While private PPP owns the modem, ordinary AT inventory would pause lwIP
	// traffic and can turn an otherwise healthy VoWiFi call into periodic audio
	// corruption. Keep the last identity/SIM fact and refresh only live data
	// ownership until DATA_DISABLE makes AT sampling safe again.
	data := dataState(current.client.LinkState())
	if data != agentmodem.DataDisconnected && current.lastFact.AttachmentID != "" {
		fact := cloneFact(current.lastFact)
		fact.AT = agentmodem.ATControlFact{State: agentmodem.ATControlUnavailable,
			Detail: "protected cellular data owns modem operations"}
		fact.Capabilities.SMSReceive, fact.Capabilities.SMSSend = false, false
		fact.Network.Data = data
		fact.Network.Profile = "private-ppp"
		return fact, nil
	}
	if !fresh && !current.lastFactAt.IsZero() && now.Sub(current.lastFactAt) < 5*time.Second {
		fact := cloneFact(current.lastFact)
		fact.Network.Data = data
		if fact.Network.Data != agentmodem.DataDisconnected {
			fact.Network.Profile = "private-ppp"
		} else {
			fact.Network.Profile = ""
		}
		return fact, nil
	}
	fact := agentmodem.Fact{
		AttachmentID: current.attachment.ID(), EquipmentID: current.owner.EquipmentID(),
		ContinuityEpoch:     current.attachment.Generation(),
		LastContinuityIssue: current.lastContinuityIssue,
		Manufacturer:        current.manufacturer, Model: current.model, Firmware: current.firmware,
		Condition: agentmodem.DeviceReady,
		Capabilities: agentmodem.Capabilities{
			CellularData: true, SMSReceive: current.owner.Capabilities().SMS, SMSSend: current.owner.Capabilities().SMS,
		},
		AT: agentmodem.ATControlFact{
			State: agentmodem.ATControlReady, Port: modemPortLabel(current.attachment),
			CallSignalling: current.owner.Capabilities().CallSignalling,
			SMS:            current.owner.Capabilities().SMS, SIMAPDU: current.owner.Capabilities().SIMAPDU,
		},
		SIM: agentmodem.SIMFact{State: agentmodem.SIMUnknown},
		Network: agentmodem.NetworkFact{
			Registration:  agentmodem.RegistrationUnknown,
			SoftwareRadio: agentmodem.RadioUnknown, HardwareRadio: agentmodem.RadioUnknown,
			Data:  dataState(current.client.LinkState()),
			Guard: agentmodem.DataGuardFact{State: agentmodem.DataGuardProtected},
		},
	}
	status, err := current.owner.SIMPINStatus(ctx)
	if err != nil {
		if simAbsent(err) {
			fact.SIM.State = agentmodem.SIMAbsent
			current.lastFact = cloneFact(fact)
			current.lastFactAt = now
			return fact, nil
		}
		fact.AT.State = agentmodem.ATControlDegraded
		fact.AT.Detail = bounded(err.Error(), 1024)
		return fact, err
	}
	fact.SIM.State = simState(string(status.State))
	fact.SIM.PINState = string(status.State)
	fact.SIM.ICCID = status.CardID
	fact.SIM.PINAttempts = cloneUint32(status.AttemptsRemaining)
	if fact.SIM.State == agentmodem.SIMReady {
		if value, queryErr := current.owner.Exchange(ctx, "AT+CIMI", 3*time.Second); queryErr == nil {
			fact.SIM.IMSI = parseIMSI(value)
		}
		if value, queryErr := current.owner.Exchange(ctx, "AT+CNUM", 3*time.Second); queryErr == nil {
			fact.SIM.MSISDNs = parseMSISDNs(value)
		}
		if value, queryErr := current.owner.Exchange(ctx, "AT+CSCA?", 3*time.Second); queryErr == nil {
			fact.SIM.SMSC = parseSMSC(value)
			fact.SIM.Configured = fact.SIM.SMSC != ""
		}
	}
	if value, queryErr := current.owner.Exchange(ctx, "AT+CREG?", 3*time.Second); queryErr == nil {
		fact.Network.Registration = parseRegistration(value)
	}
	if fact.Network.Registration == agentmodem.RegistrationUnknown {
		if value, queryErr := current.owner.Exchange(ctx, "AT+CEREG?", 3*time.Second); queryErr == nil {
			fact.Network.Registration = parseRegistration(value)
		}
	}
	if value, queryErr := current.owner.Exchange(ctx, "AT+COPS?", 3*time.Second); queryErr == nil {
		fact.Network.OperatorID, fact.Network.OperatorName = parseOperator(value)
	}
	if value, queryErr := current.owner.Exchange(ctx, "AT+CSQ", 3*time.Second); queryErr == nil {
		fact.Network.SignalPercent = parseSignal(value)
	}
	if value, queryErr := current.owner.Exchange(ctx, "AT+CFUN?", 3*time.Second); queryErr == nil {
		fact.Network.SoftwareRadio = parseRadio(value)
	}
	if fact.Network.Data != agentmodem.DataDisconnected {
		fact.Network.Profile = "private-ppp"
	}
	current.lastFact = cloneFact(fact)
	current.lastFactAt = now
	return fact, nil
}

func modemPortLabel(attachment cellulario.Attachment) string { return attachment.ID() }

func (prober *Prober) Operate(ctx context.Context, operation agentmodem.Operation) (agentmodem.OperationResult, error) {
	prober.mu.Lock()
	defer prober.mu.Unlock()
	facts, err := prober.probeLocked(ctx, true)
	if err != nil {
		return agentmodem.OperationResult{}, err
	}
	if err := agentmodem.ValidateOperationTarget(facts, operation); err != nil {
		return agentmodem.OperationResult{}, err
	}
	current := prober.find(operation.AttachmentID, operation.EquipmentID)
	if current == nil {
		return agentmodem.OperationResult{}, agentmodem.ErrOperationTargetReplaced
	}
	if privateDataOwnsSIM(current) && operation.Action != agentmodem.OperationCallStatus &&
		operation.Action != agentmodem.OperationCallHangup {
		return agentmodem.OperationResult{}, fmt.Errorf("%w: private cellular data owns SIM operations", agentmodem.ErrOperationUnavailable)
	}
	if operation.Action == agentmodem.OperationSMSList {
		messages, err := current.owner.ListSMS(ctx, operation.EquipmentID)
		if err != nil {
			return agentmodem.OperationResult{}, err
		}
		result := make([]agentmodem.SMSMessage, 0, len(messages))
		for _, message := range messages {
			result = append(result, agentmodem.SMSMessage{
				Index: message.Index, Indices: append([]int(nil), message.Indices...), State: message.State, Direction: message.Direction,
				Peer: message.Peer, Body: message.Body, ObservedAt: message.ObservedAt,
				Fingerprint: message.Fingerprint, Reference: message.Reference, Delivery: message.DeliveryState,
			})
		}
		return agentmodem.OperationResult{SMS: agentmodem.SMSResult{State: "listed", Messages: result}}, nil
	}
	if operation.Action == agentmodem.OperationSMSSend {
		references, err := current.owner.SendSMS(ctx, operation.EquipmentID, operation.Number, operation.Body)
		if err != nil {
			return agentmodem.OperationResult{SMS: agentmodem.SMSResult{References: references}}, err
		}
		return agentmodem.OperationResult{SMS: agentmodem.SMSResult{State: "submitted", References: references}}, nil
	}
	if operation.Action == agentmodem.OperationSMSDelete {
		if err := current.owner.DeleteSMS(ctx, operation.EquipmentID, operation.SMSIndices, operation.SMSFingerprint); err != nil {
			if errors.Is(err, agentat.ErrSMSIdentityChanged) {
				return agentmodem.OperationResult{}, agentmodem.ErrSMSStorageChanged
			}
			return agentmodem.OperationResult{}, err
		}
		return agentmodem.OperationResult{SMS: agentmodem.SMSResult{State: "deleted"}}, nil
	}
	var call agentat.CallState
	switch operation.Action {
	case agentmodem.OperationCallStatus:
		call, err = current.owner.CallStatus(ctx)
	case agentmodem.OperationCallHangup:
		call, err = current.owner.VerifiedHangup(ctx)
	case agentmodem.OperationCallDial:
		call, err = current.owner.Dial(ctx, operation.Number)
	case agentmodem.OperationCallAnswer:
		call, err = current.owner.AnswerIncoming(ctx, agentat.IncomingCallFence{
			NativeIndex: operation.NativeCallIndex, Number: operation.Number,
		})
	case agentmodem.OperationCallReject:
		call, err = current.owner.RejectIncoming(ctx, agentat.IncomingCallFence{
			NativeIndex: operation.NativeCallIndex, Number: operation.Number,
		})
	case agentmodem.OperationCallDTMF:
		call, err = current.owner.SendDTMF(ctx, operation.Signal)
	default:
		return agentmodem.OperationResult{}, errors.New("unsupported modem operation")
	}
	if err != nil {
		if errors.Is(err, agentat.ErrIncomingCallChanged) {
			return agentmodem.OperationResult{}, agentmodem.ErrIncomingCallChanged
		}
		return agentmodem.OperationResult{}, err
	}
	return agentmodem.OperationResult{Call: agentmodem.CallResult{
		State: call.State, Direction: call.Direction, Number: call.Number,
		NativeIndex: call.NativeIndex, VoiceCalls: call.VoiceCalls, IncomingCalls: call.IncomingCalls,
		ObservedAt: call.ObservedAt, Authoritative: call.Authoritative,
		TerminalConfirmed: call.TerminalConfirmed, Strategy: call.Strategy,
	}}, nil
}

func (prober *Prober) AuthenticateSIMAKA(ctx context.Context, request agentmodem.SIMAKARequest) (agentmodem.SIMAKAResult, error) {
	prober.mu.Lock()
	defer prober.mu.Unlock()
	facts, err := prober.probeLocked(ctx, true)
	if err != nil {
		return agentmodem.SIMAKAResult{}, err
	}
	if err := agentmodem.ValidateSIMAKATarget(facts, request); err != nil {
		return agentmodem.SIMAKAResult{}, err
	}
	current := prober.find(request.AttachmentID, request.EquipmentID)
	if current == nil {
		return agentmodem.SIMAKAResult{}, agentmodem.ErrOperationTargetReplaced
	}
	if privateDataOwnsSIM(current) {
		return agentmodem.SIMAKAResult{}, fmt.Errorf("%w: private cellular data owns SIM operations", agentmodem.ErrOperationUnavailable)
	}
	call, err := current.owner.CallStatus(ctx)
	if err != nil || !call.Authoritative || call.State != "idle" {
		if err != nil {
			return agentmodem.SIMAKAResult{}, err
		}
		return agentmodem.SIMAKAResult{}, agentmodem.ErrOperationUnavailable
	}
	result, err := current.owner.AuthenticateAKA(ctx, request.Application, request.RAND, request.AUTN)
	return agentmodem.SIMAKAResult{Body: result.Body, SW1: result.SW1, SW2: result.SW2}, err
}

func (prober *Prober) EnterSIMPIN(ctx context.Context, request agentmodem.SIMPINRequest) (agentmodem.SIMPINResult, error) {
	prober.mu.Lock()
	defer prober.mu.Unlock()
	facts, err := prober.probeLocked(ctx, true)
	if err != nil {
		return agentmodem.SIMPINResult{}, err
	}
	var target *agentmodem.Fact
	for index := range facts {
		if facts[index].AttachmentID == request.AttachmentID && facts[index].EquipmentID == request.EquipmentID &&
			facts[index].SIM.ICCID == request.CardID {
			if target != nil {
				return agentmodem.SIMPINResult{}, agentmodem.ErrOperationTargetReplaced
			}
			target = &facts[index]
		}
	}
	if target == nil {
		return agentmodem.SIMPINResult{}, agentmodem.ErrOperationTargetReplaced
	}
	if target.SIM.State != agentmodem.SIMLocked || target.SIM.PINState != string(agentat.SIMPINRequired) {
		return agentmodem.SIMPINResult{}, agentmodem.ErrOperationUnavailable
	}
	current := prober.find(request.AttachmentID, request.EquipmentID)
	if current == nil {
		return agentmodem.SIMPINResult{}, agentmodem.ErrOperationTargetReplaced
	}
	if privateDataOwnsSIM(current) {
		return agentmodem.SIMPINResult{}, fmt.Errorf("%w: private cellular data owns SIM operations", agentmodem.ErrOperationUnavailable)
	}
	result, err := current.owner.EnterSIMPIN(ctx, request.CardID, request.PIN)
	current.lastFactAt = time.Time{}
	return agentmodem.SIMPINResult{
		Attempted: result.Attempted, Ready: result.Status.State == agentat.SIMPINNotRequired,
		AttemptsRemaining: cloneUint32(result.Status.AttemptsRemaining),
	}, err
}

func (prober *Prober) PrepareData(ctx context.Context, target agentdata.Target, profile agentdata.Profile) (string, error) {
	prober.mu.Lock()
	defer prober.mu.Unlock()
	current, err := prober.dataTarget(ctx, target)
	if err != nil {
		return "", err
	}
	call, err := current.owner.CallStatus(ctx)
	if err != nil {
		return "", err
	}
	if !call.Authoritative || call.State != "idle" {
		return "", errors.New("paid voice call is active")
	}
	if err := current.client.Qualify(ctx); err != nil {
		return "", fmt.Errorf("isolation_not_proven: %w", err)
	}
	if err := current.client.EnableData(ctx); err != nil {
		return "", err
	}
	name := strings.TrimSpace(profile.Name)
	if name == "" {
		name = "private-ppp"
	}
	return name, nil
}

func (prober *Prober) DialData(ctx context.Context, target agentdata.Target, network, address string) (net.Conn, error) {
	prober.mu.Lock()
	defer prober.mu.Unlock()
	current, err := prober.dataTarget(ctx, target)
	if err != nil {
		return nil, err
	}
	if current.client.LinkState() != "up" {
		return nil, errors.New("private cellular PPP link is not ready")
	}
	switch network {
	case "tcp":
		return current.client.OpenTCP(ctx, address)
	case "udp":
		return current.client.OpenUDP(ctx, address)
	default:
		return nil, errors.New("unsupported private cellular network")
	}
}

func (prober *Prober) StopData(ctx context.Context, target agentdata.Target) error {
	prober.mu.Lock()
	defer prober.mu.Unlock()
	current := prober.find(target.AttachmentID, target.EquipmentID)
	if current == nil {
		return agentmodem.ErrOperationTargetReplaced
	}
	// Cleanup revokes only the private helper link already owned by this
	// attachment. It must remain possible after a SIM replacement, when the
	// old CardID generation can no longer pass a fresh admission probe.
	return current.client.DisableData(ctx)
}

func (prober *Prober) dataTarget(ctx context.Context, target agentdata.Target) (*device, error) {
	facts, err := prober.probeLocked(ctx, true)
	if err != nil {
		return nil, err
	}
	matches := 0
	for _, fact := range facts {
		if fact.AttachmentID == target.AttachmentID && fact.EquipmentID == target.EquipmentID &&
			fact.SIM.ICCID == target.CardID && fact.SIM.State == agentmodem.SIMReady &&
			(target.SIMSessionGeneration == "" || fact.SIM.SessionGeneration == target.SIMSessionGeneration) &&
			fact.Capabilities.CellularData && fact.Network.Guard.State == agentmodem.DataGuardProtected {
			matches++
		}
	}
	if matches != 1 {
		return nil, agentmodem.ErrOperationTargetReplaced
	}
	current := prober.find(target.AttachmentID, target.EquipmentID)
	if current == nil {
		return nil, agentmodem.ErrOperationTargetReplaced
	}
	return current, nil
}

func (prober *Prober) find(attachmentID, equipmentID string) *device {
	for _, current := range prober.devices {
		if current.attachment.ID() == attachmentID && current.owner.EquipmentID() == equipmentID {
			return current
		}
	}
	return nil
}

func (prober *Prober) pruneDisconnected() {
	for generation, current := range prober.devices {
		if current.client.Alive() {
			continue
		}
		_ = current.owner.Close()
		delete(prober.devices, generation)
	}
}

func (prober *Prober) recordRetry(generation string, err error) {
	current := prober.retries[generation]
	current.attempt++
	shift := min(current.attempt-1, 5)
	delay := time.Second * time.Duration(1<<shift)
	if delay > 30*time.Second {
		delay = 30 * time.Second
	}
	current.next = prober.now().Add(delay)
	current.detail = bounded(err.Error(), 1024)
	prober.retries[generation] = current
}

func (prober *Prober) Close() error {
	prober.mu.Lock()
	defer prober.mu.Unlock()
	var failures []error
	for generation, current := range prober.devices {
		failures = append(failures, current.owner.Close())
		delete(prober.devices, generation)
	}
	return errors.Join(failures...)
}

type helperPort struct {
	mu     sync.Mutex
	client *cellulario.Client
	buffer []byte
	closed bool
}

func (port *helperPort) Exchange(ctx context.Context, command string, timeout time.Duration) ([]byte, error) {
	port.mu.Lock()
	defer port.mu.Unlock()
	if port.closed {
		return nil, errors.New("private USB modem is closed")
	}
	return port.client.AT(ctx, command, timeout)
}

func (port *helperPort) Write(value []byte) (int, error) {
	port.mu.Lock()
	defer port.mu.Unlock()
	if port.closed {
		return 0, errors.New("private USB modem is closed")
	}
	if len(value) < 3 || value[len(value)-1] != '\r' || !strings.HasPrefix(string(value), "AT") {
		return 0, errors.New("private USB modem accepts only bounded AT transactions")
	}
	response, err := port.client.AT(context.Background(), string(value[:len(value)-1]), 30*time.Second)
	if err != nil {
		return 0, err
	}
	port.buffer = append(port.buffer[:0], response...)
	return len(value), nil
}

func (port *helperPort) Read(value []byte) (int, error) {
	port.mu.Lock()
	defer port.mu.Unlock()
	if port.closed {
		return 0, io.EOF
	}
	count := copy(value, port.buffer)
	port.buffer = port.buffer[count:]
	return count, nil
}

func (port *helperPort) ResetInputBuffer() error {
	port.mu.Lock()
	defer port.mu.Unlock()
	port.buffer = port.buffer[:0]
	return nil
}

func (*helperPort) Drain() error { return nil }

func (port *helperPort) SubmitSMSPDU(ctx context.Context, length int, payload string) ([]byte, bool, error) {
	port.mu.Lock()
	defer port.mu.Unlock()
	if port.closed {
		return nil, false, errors.New("private USB modem is closed")
	}
	return port.client.SubmitSMSPDU(ctx, length, payload)
}

func (port *helperPort) Close() error {
	port.mu.Lock()
	if port.closed {
		port.mu.Unlock()
		return nil
	}
	port.closed = true
	port.mu.Unlock()
	return port.client.Close()
}

func optionalInformation(ctx context.Context, owner *agentat.Owner, command string) string {
	value, err := owner.Exchange(ctx, command, 3*time.Second)
	if err != nil {
		return ""
	}
	return firstInformationLine(value, command)
}

func unavailableFact(attachment cellulario.Attachment, detail string) agentmodem.Fact {
	return agentmodem.Fact{
		AttachmentID: attachment.ID(), ContinuityEpoch: attachment.Generation(),
		Condition: agentmodem.DeviceDegraded, Detail: bounded(detail, 1024),
		AT:  agentmodem.ATControlFact{State: agentmodem.ATControlUnavailable, Detail: bounded(detail, 1024)},
		SIM: agentmodem.SIMFact{State: agentmodem.SIMUnknown},
		Network: agentmodem.NetworkFact{
			Registration:  agentmodem.RegistrationUnknown,
			SoftwareRadio: agentmodem.RadioUnknown, HardwareRadio: agentmodem.RadioUnknown,
			Data:  agentmodem.DataUnknown,
			Guard: agentmodem.DataGuardFact{State: agentmodem.DataGuardFailed, Detail: bounded(detail, 1024)},
		},
	}
}

func dataState(value string) agentmodem.DataState {
	switch value {
	case "up":
		return agentmodem.DataConnected
	case "connecting":
		return agentmodem.DataConnecting
	default:
		return agentmodem.DataDisconnected
	}
}

func privateDataOwnsSIM(current *device) bool {
	return current != nil && current.client.LinkState() != "down"
}

func simAbsent(err error) bool {
	detail := strings.ToLower(err.Error())
	return strings.Contains(detail, "sim not inserted") || strings.Contains(detail, "sim absent") ||
		strings.Contains(detail, "no sim") || simAbsentCME.MatchString(detail)
}

func cloneUint32(value *uint32) *uint32 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneFact(source agentmodem.Fact) agentmodem.Fact {
	result := source
	result.SIM.MSISDNs = append([]string(nil), source.SIM.MSISDNs...)
	result.SIM.PINAttempts = cloneUint32(source.SIM.PINAttempts)
	result.Network.SignalPercent = cloneUint32(source.Network.SignalPercent)
	return result
}
