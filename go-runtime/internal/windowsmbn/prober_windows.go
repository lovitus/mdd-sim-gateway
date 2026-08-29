//go:build windows && (amd64 || arm64)

// Package windowsmbn implements read-only Windows Mobile Broadband facts.
package windowsmbn

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"unsafe"

	win32 "github.com/deploymenttheory/go-bindings-win32/bindings/runtime/win32"
	"github.com/deploymenttheory/go-bindings-win32/bindings/win32/foundation"
	mbn "github.com/deploymenttheory/go-bindings-win32/bindings/win32/networkmanagement/mobilebroadband"
	"github.com/deploymenttheory/go-bindings-win32/bindings/win32/system/com"
	"github.com/deploymenttheory/go-bindings-win32/bindings/win32/system/ole"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentat"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/windowsat"
)

// Microsoft does not currently publish coclass IDs through win32metadata.
// This is the Windows SDK MbnInterfaceManager CLSID used by mbnapi.tlb.
var clsidMbnInterfaceManager = win32.GUID{
	Data1: 0xbdfee05b, Data2: 0x4418, Data3: 0x11dd,
	Data4: [8]byte{0x90, 0xed, 0x00, 0x1c, 0x25, 0x7c, 0xcf, 0xf1},
}

type Prober struct {
	at *agentat.Manager
}

func NewProber() (*Prober, error) {
	manager, err := windowsat.NewManager()
	if err != nil {
		return nil, err
	}
	return &Prober{at: manager}, nil
}

// Probe executes in one COM apartment and releases every COM/BSTR/SAFEARRAY
// allocation before returning. It performs no modem mutation.
func (prober *Prober) Probe(ctx context.Context) ([]agentmodem.Fact, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if _, err := com.CoInitializeEx(uint32(com.COINIT_MULTITHREADED)); err != nil {
		return nil, fmt.Errorf("initialize Windows MBN COM apartment: %w", err)
	}
	defer com.CoUninitialize()

	var root *win32.IUnknown
	if err := com.CoCreateInstance(
		&clsidMbnInterfaceManager, nil, com.CLSCTX_INPROC_SERVER,
		&mbn.IID_IMbnInterfaceManager, &root,
	); err != nil {
		if root != nil {
			root.Release()
		}
		return nil, fmt.Errorf("create Windows MBN interface manager: %w", err)
	}
	manager := win32.Cast[mbn.IMbnInterfaceManager](root)
	defer manager.Release()

	var interfaces *com.SAFEARRAY
	if err := manager.GetInterfaces(&interfaces); err != nil {
		if interfaces != nil {
			ole.SafeArrayDestroy(interfaces)
		}
		// Windows 7-11 return HRESULT_FROM_WIN32(ERROR_NOT_FOUND) when no
		// Mobile Broadband interface exists, despite the older API table not
		// documenting that result. No attachment is a successful empty probe.
		if errors.Is(err, syscall.Errno(foundation.ERROR_NOT_FOUND)) {
			facts := []agentmodem.Fact{}
			prober.reconcileAT(ctx, facts)
			return facts, nil
		}
		return nil, fmt.Errorf("enumerate Windows MBN interfaces: %w", err)
	}
	if interfaces == nil {
		facts := []agentmodem.Fact{}
		prober.reconcileAT(ctx, facts)
		return facts, nil
	}
	defer ole.SafeArrayDestroy(interfaces)

	lower, upper, err := bounds(interfaces)
	if err != nil {
		return nil, err
	}
	facts := make([]agentmodem.Fact, 0, max(0, int(upper-lower+1)))
	for index := lower; index <= upper; index++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var current *mbn.IMbnInterface
		if err := ole.SafeArrayGetElement(interfaces, &index, unsafe.Pointer(&current)); err != nil {
			if current != nil {
				current.Release()
			}
			return nil, fmt.Errorf("read Windows MBN interface array: %w", err)
		}
		if current == nil {
			continue
		}
		fact := probeInterface(current, index)
		current.Release()
		facts = append(facts, fact)
	}
	sort.Slice(facts, func(left, right int) bool { return facts[left].AttachmentID < facts[right].AttachmentID })
	prober.reconcileAT(ctx, facts)
	return facts, nil
}

func (prober *Prober) Close() error {
	if prober.at == nil {
		return nil
	}
	return prober.at.Close()
}

func (prober *Prober) reconcileAT(ctx context.Context, facts []agentmodem.Fact) {
	if prober.at == nil {
		for index := range facts {
			facts[index].AT = agentmodem.ATControlFact{State: agentmodem.ATControlUnknown}
		}
		return
	}
	targets := make([]agentat.Target, 0, len(facts))
	for _, fact := range facts {
		targets = append(targets, agentat.Target{AttachmentID: fact.AttachmentID, EquipmentID: fact.EquipmentID})
	}
	snapshots := prober.at.Reconcile(ctx, targets)
	for index := range facts {
		snapshot, exists := snapshots[facts[index].AttachmentID]
		if !exists {
			facts[index].AT = agentmodem.ATControlFact{State: agentmodem.ATControlUnknown}
			continue
		}
		facts[index].AT = agentmodem.ATControlFact{
			State: agentmodem.ATControlState(snapshot.State), Port: snapshot.Port, Detail: snapshot.Detail,
			CallSignalling: snapshot.CallSignalling, SMS: snapshot.SMS, SIMAPDU: snapshot.SIMAPDU,
		}
	}
}

func probeInterface(value *mbn.IMbnInterface, arrayIndex int32) agentmodem.Fact {
	fact := agentmodem.Fact{
		Condition: agentmodem.DeviceReady,
		SIM:       agentmodem.SIMFact{State: agentmodem.SIMUnknown},
		Network: agentmodem.NetworkFact{
			Registration:  agentmodem.RegistrationUnknown,
			SoftwareRadio: agentmodem.RadioUnknown, HardwareRadio: agentmodem.RadioUnknown,
			Data: agentmodem.DataUnknown,
		},
	}
	var failures []string

	var attachment foundation.BSTR
	if err := value.Get_InterfaceID(&attachment); err != nil {
		failures = append(failures, "interface_id: "+err.Error())
	} else {
		fact.AttachmentID = normalizeAttachmentID(takeBSTR(attachment))
	}

	var ready mbn.MBN_READY_STATE
	if err := value.GetReadyState(&ready); err != nil {
		failures = append(failures, "ready_state: "+err.Error())
	} else {
		fact.SIM.State = simState(ready)
	}

	var caps mbn.MBN_INTERFACE_CAPS
	if err := value.GetInterfaceCapability(&caps); err != nil {
		failures = append(failures, "capabilities: "+err.Error())
	} else {
		fact.EquipmentID = takeBSTR(caps.DeviceID)
		fact.Manufacturer = takeBSTR(caps.Manufacturer)
		fact.Model = takeBSTR(caps.Model)
		fact.Firmware = takeBSTR(caps.FirmwareInfo)
		takeBSTR(caps.CustomDataClass)
		takeBSTR(caps.CustomBandClass)
		fact.Capabilities = agentmodem.Capabilities{
			CellularData:  caps.DataClass != 0,
			SMSReceive:    caps.SmsCaps&uint32(mbn.MBN_SMS_CAPS_PDU_RECEIVE) != 0,
			SMSSend:       caps.SmsCaps&uint32(mbn.MBN_SMS_CAPS_PDU_SEND) != 0,
			MBNVoiceClass: voiceClass(caps.VoiceClass),
		}
	}

	if fact.SIM.State == agentmodem.SIMReady {
		subscriber, err := value.GetSubscriberInformation()
		if err != nil {
			if subscriber != nil {
				subscriber.Release()
			}
			failures = append(failures, "subscriber: "+err.Error())
		} else if subscriber != nil {
			fact.SIM.ICCID = readBSTR(subscriber.Get_SimIccID)
			fact.SIM.IMSI = readBSTR(subscriber.Get_SubscriberID)
			if values, err := readBSTRArray(subscriber); err != nil {
				failures = append(failures, "telephone_numbers: "+err.Error())
			} else {
				fact.SIM.MSISDNs = values
			}
			subscriber.Release()
		}
	}

	probeNetwork(value, &fact, &failures)
	probeSMS(value, &fact)
	if fact.AttachmentID == "" {
		fact.AttachmentID = fmt.Sprintf("mbn-unidentified-%d", arrayIndex)
	}
	if len(failures) != 0 {
		fact.Condition = agentmodem.DeviceDegraded
		fact.Detail = bounded(strings.Join(failures, "; "))
	}
	return fact
}

func probeNetwork(value *mbn.IMbnInterface, fact *agentmodem.Fact, failures *[]string) {
	if registration, err := query[mbn.IMbnRegistration](value, &mbn.IID_IMbnRegistration); err == nil {
		var state mbn.MBN_REGISTER_STATE
		if err := registration.GetRegisterState(&state); err == nil {
			fact.Network.Registration = registrationState(state)
		} else {
			*failures = append(*failures, "registration: "+err.Error())
		}
		fact.Network.OperatorID = readBSTR(registration.GetProviderID)
		fact.Network.OperatorName = readBSTR(registration.GetProviderName)
		registration.Release()
	} else {
		*failures = append(*failures, "registration_interface: "+err.Error())
	}
	if signal, err := query[mbn.IMbnSignal](value, &mbn.IID_IMbnSignal); err == nil {
		var percent uint32
		if signal.GetSignalStrength(&percent) == nil && percent <= 100 {
			fact.Network.SignalPercent = &percent
		}
		signal.Release()
	}
	if radio, err := query[mbn.IMbnRadio](value, &mbn.IID_IMbnRadio); err == nil {
		var software, hardware mbn.MBN_RADIO
		if radio.Get_SoftwareRadioState(&software) == nil {
			fact.Network.SoftwareRadio = radioState(software)
		}
		if radio.Get_HardwareRadioState(&hardware) == nil {
			fact.Network.HardwareRadio = radioState(hardware)
		}
		radio.Release()
	}
	connection, err := value.GetConnection()
	if err == nil && connection != nil {
		var state mbn.MBN_ACTIVATION_STATE
		var profile foundation.BSTR
		if err := connection.GetConnectionState(&state, &profile); err == nil {
			fact.Network.Data = dataState(state)
			fact.Network.Profile = takeBSTR(profile)
		} else {
			*failures = append(*failures, "connection_state: "+err.Error())
		}
		connection.Release()
	} else if err != nil {
		if connection != nil {
			connection.Release()
		}
		*failures = append(*failures, "connection: "+err.Error())
	}
}

func probeSMS(value *mbn.IMbnInterface, fact *agentmodem.Fact) {
	if !fact.Capabilities.SMSReceive && !fact.Capabilities.SMSSend {
		return
	}
	sms, err := query[mbn.IMbnSms](value, &mbn.IID_IMbnSms)
	if err != nil {
		fact.SIM.SMSError = bounded(err.Error())
		return
	}
	defer sms.Release()
	var configuration *mbn.IMbnSmsConfiguration
	err = sms.GetSmsConfiguration(&configuration)
	if err != nil || configuration == nil {
		if configuration != nil {
			configuration.Release()
		}
		if err != nil {
			fact.SIM.SMSError = bounded(err.Error())
		}
		return
	}
	defer configuration.Release()
	fact.SIM.Configured = true
	fact.SIM.SMSC = readBSTR(configuration.Get_ServiceCenterAddress)
}

func query[T any](source *mbn.IMbnInterface, iid *win32.GUID) (*T, error) {
	var unknown *win32.IUnknown
	if err := source.QueryInterface(iid, &unknown); err != nil {
		if unknown != nil {
			unknown.Release()
		}
		return nil, err
	}
	return win32.Cast[T](unknown), nil
}

func bounds(array *com.SAFEARRAY) (int32, int32, error) {
	var lower, upper int32
	if err := ole.SafeArrayGetLBound(array, 1, &lower); err != nil {
		return 0, 0, err
	}
	if err := ole.SafeArrayGetUBound(array, 1, &upper); err != nil {
		return 0, 0, err
	}
	return lower, upper, nil
}

func readBSTR(get func(*foundation.BSTR) error) string {
	var value foundation.BSTR
	if get(&value) != nil {
		return ""
	}
	return takeBSTR(value)
}

func takeBSTR(value foundation.BSTR) string {
	if value == nil {
		return ""
	}
	result := win32.UTF16ToString(value)
	foundation.SysFreeString(value)
	return result
}

func readBSTRArray(subscriber *mbn.IMbnSubscriberInformation) ([]string, error) {
	var array *com.SAFEARRAY
	if err := subscriber.Get_TelephoneNumbers(&array); err != nil {
		return nil, err
	}
	if array == nil {
		return []string{}, nil
	}
	defer ole.SafeArrayDestroy(array)
	lower, upper, err := bounds(array)
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, max(0, int(upper-lower+1)))
	for index := lower; index <= upper; index++ {
		var value foundation.BSTR
		if err := ole.SafeArrayGetElement(array, &index, unsafe.Pointer(&value)); err != nil {
			return nil, err
		}
		if text := takeBSTR(value); text != "" {
			result = append(result, text)
		}
	}
	return result, nil
}

func simState(value mbn.MBN_READY_STATE) agentmodem.SIMState {
	switch value {
	case mbn.MBN_READY_STATE_INITIALIZED:
		return agentmodem.SIMReady
	case mbn.MBN_READY_STATE_SIM_NOT_INSERTED, mbn.MBN_READY_STATE_NO_ESIM_PROFILE:
		return agentmodem.SIMAbsent
	case mbn.MBN_READY_STATE_DEVICE_LOCKED, mbn.MBN_READY_STATE_DEVICE_BLOCKED:
		return agentmodem.SIMLocked
	case mbn.MBN_READY_STATE_BAD_SIM, mbn.MBN_READY_STATE_FAILURE:
		return agentmodem.SIMFailed
	default:
		return agentmodem.SIMUnknown
	}
}

func registrationState(value mbn.MBN_REGISTER_STATE) agentmodem.RegistrationState {
	switch value {
	case mbn.MBN_REGISTER_STATE_DEREGISTERED:
		return agentmodem.RegistrationUnregistered
	case mbn.MBN_REGISTER_STATE_SEARCHING:
		return agentmodem.RegistrationSearching
	case mbn.MBN_REGISTER_STATE_HOME:
		return agentmodem.RegistrationHome
	case mbn.MBN_REGISTER_STATE_ROAMING, mbn.MBN_REGISTER_STATE_PARTNER:
		return agentmodem.RegistrationRoaming
	case mbn.MBN_REGISTER_STATE_DENIED:
		return agentmodem.RegistrationDenied
	default:
		return agentmodem.RegistrationUnknown
	}
}

func radioState(value mbn.MBN_RADIO) agentmodem.RadioState {
	if value == mbn.MBN_RADIO_ON {
		return agentmodem.RadioOn
	}
	if value == mbn.MBN_RADIO_OFF {
		return agentmodem.RadioOff
	}
	return agentmodem.RadioUnknown
}

func dataState(value mbn.MBN_ACTIVATION_STATE) agentmodem.DataState {
	switch value {
	case mbn.MBN_ACTIVATION_STATE_ACTIVATED:
		return agentmodem.DataConnected
	case mbn.MBN_ACTIVATION_STATE_ACTIVATING:
		return agentmodem.DataConnecting
	case mbn.MBN_ACTIVATION_STATE_DEACTIVATED:
		return agentmodem.DataDisconnected
	case mbn.MBN_ACTIVATION_STATE_DEACTIVATING:
		return agentmodem.DataDisconnecting
	default:
		return agentmodem.DataUnknown
	}
}

func voiceClass(value mbn.MBN_VOICE_CLASS) string {
	switch value {
	case mbn.MBN_VOICE_CLASS_NO_VOICE:
		return "no_voice"
	case mbn.MBN_VOICE_CLASS_SEPARATE_VOICE_DATA:
		return "separate_voice_data"
	case mbn.MBN_VOICE_CLASS_SIMULTANEOUS_VOICE_DATA:
		return "simultaneous_voice_data"
	default:
		return "unknown"
	}
}

func normalizeAttachmentID(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "{")
	value = strings.TrimSuffix(value, "}")
	var result strings.Builder
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || strings.ContainsRune("-_.:", character) {
			result.WriteRune(character)
		}
	}
	return strings.ToLower(result.String())
}

func bounded(value string) string {
	value = strings.ToValidUTF8(value, "?")
	if len(value) > 1024 {
		value = strings.ToValidUTF8(value[:1024], "?")
	}
	return value
}
