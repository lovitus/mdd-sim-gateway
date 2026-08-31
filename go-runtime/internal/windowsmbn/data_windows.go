//go:build windows && (amd64 || arm64)

package windowsmbn

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	win32 "github.com/deploymenttheory/go-bindings-win32/bindings/runtime/win32"
	"github.com/deploymenttheory/go-bindings-win32/bindings/win32/foundation"
	mbn "github.com/deploymenttheory/go-bindings-win32/bindings/win32/networkmanagement/mobilebroadband"
	"github.com/deploymenttheory/go-bindings-win32/bindings/win32/system/com"
	"github.com/deploymenttheory/go-bindings-win32/bindings/win32/system/ole"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentdata"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentmodem"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/windowsdataguard"
	"golang.org/x/sys/windows"
)

var clsidMbnConnectionProfileManager = win32.GUID{
	Data1: 0xbdfee05a, Data2: 0x4418, Data3: 0x11dd,
	Data4: [8]byte{0x90, 0xed, 0x00, 0x1c, 0x25, 0x7c, 0xcf, 0xf1},
}

const socketOptionUnicastInterface = 31

type dataBorrow struct {
	target  agentdata.Target
	profile string
	borrow  *windowsdataguard.Borrow
}

type mbnProfileXML struct {
	Name      string `xml:"Name"`
	IsDefault bool   `xml:"IsDefault"`
}

func (prober *Prober) PrepareData(ctx context.Context, target agentdata.Target, requestedProfile string) (string, error) {
	prober.mu.Lock()
	defer prober.mu.Unlock()
	if prober.guard == nil {
		return "", errors.New("persistent cellular data guard is unavailable")
	}
	if current := prober.data[target.EquipmentID]; current != nil {
		if current.target == target && (requestedProfile == "" || requestedProfile == current.profile) {
			return current.profile, nil
		}
		return "", errors.New("another cellular data session owns this modem")
	}
	facts, err := prober.probeLocked(ctx)
	if err != nil {
		return "", err
	}
	if !matchesDataTarget(facts, target) {
		return "", agentmodem.ErrOperationTargetReplaced
	}
	if err := prober.requireFreshReadyCard(ctx, target.EquipmentID, target.CardID); err != nil {
		return "", err
	}
	call, err := prober.at.CallStatus(ctx, target.EquipmentID)
	if err == nil && call.State != "idle" {
		return "", errors.New("paid voice call is active")
	}
	borrow, err := prober.guard.BeginBorrow(ctx, target.AttachmentID)
	if err != nil {
		return "", err
	}
	profile, err := connectData(ctx, target.AttachmentID, requestedProfile)
	if err != nil {
		permitErr := borrow.Close()
		cleanupContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		disconnectErr := disconnectData(cleanupContext, target.AttachmentID)
		cancel()
		return "", errors.Join(err, permitErr, disconnectErr)
	}
	prober.data[target.EquipmentID] = &dataBorrow{target: target, profile: profile, borrow: borrow}
	return profile, nil
}

func (prober *Prober) DialData(ctx context.Context, target agentdata.Target, network, address string) (net.Conn, error) {
	prober.mu.Lock()
	defer prober.mu.Unlock()
	current := prober.data[target.EquipmentID]
	if current == nil || current.target != target {
		return nil, agentmodem.ErrOperationTargetReplaced
	}
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	portValue, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || portValue == 0 {
		return nil, errors.New("invalid destination port")
	}
	index, err := current.borrow.InterfaceIndex()
	if err != nil {
		return nil, err
	}
	interfaceValue, err := net.InterfaceByIndex(int(index))
	if err != nil {
		return nil, err
	}
	sources, err := interfaceSources(interfaceValue)
	if err != nil {
		return nil, err
	}
	destinations, err := resolveDataDestination(ctx, host)
	if err != nil {
		return nil, err
	}
	var failures []error
	for _, destination := range destinations {
		source, ok := sources[destination.Is6()]
		if !ok {
			continue
		}
		if err := current.borrow.Permit(ctx, network, destination, uint16(portValue)); err != nil {
			failures = append(failures, err)
			continue
		}
		dialer := net.Dialer{Timeout: 15 * time.Second, LocalAddr: localAddress(network, source, interfaceValue.Name),
			Control: bindInterfaceControl(index, destination.Is6())}
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(destination.String(), portText))
		if err == nil {
			return conn, nil
		}
		failures = append(failures, err)
	}
	if len(failures) == 0 {
		return nil, errors.New("cellular interface has no address matching the destination")
	}
	return nil, errors.Join(failures...)
}

func (prober *Prober) StopData(ctx context.Context, target agentdata.Target) error {
	prober.mu.Lock()
	defer prober.mu.Unlock()
	current := prober.data[target.EquipmentID]
	if current == nil {
		return nil
	}
	if current.target != target {
		return agentmodem.ErrOperationTargetReplaced
	}
	delete(prober.data, target.EquipmentID)
	// Remove all traffic permits before asking MBN to tear down the PDP context.
	permitErr := current.borrow.Close()
	disconnectErr := disconnectData(ctx, target.AttachmentID)
	return errors.Join(permitErr, disconnectErr)
}

func matchesDataTarget(facts []agentmodem.Fact, target agentdata.Target) bool {
	matches := 0
	for _, fact := range facts {
		if fact.AttachmentID == target.AttachmentID && fact.EquipmentID == target.EquipmentID && fact.SIM.ICCID == target.CardID &&
			fact.SIM.State == agentmodem.SIMReady && fact.Capabilities.CellularData && fact.Network.Guard.State == agentmodem.DataGuardProtected {
			matches++
		}
	}
	return matches == 1
}

func connectData(ctx context.Context, attachmentID, requested string) (string, error) {
	return withMBNInterface(ctx, attachmentID, func(value *mbn.IMbnInterface) (string, error) {
		connection, err := value.GetConnection()
		if err != nil || connection == nil {
			if connection != nil {
				connection.Release()
			}
			return "", fmt.Errorf("get MBN connection: %w", err)
		}
		defer connection.Release()
		var state mbn.MBN_ACTIVATION_STATE
		var currentName foundation.BSTR
		if err := connection.GetConnectionState(&state, &currentName); err != nil {
			return "", err
		}
		currentProfile := takeBSTR(currentName)
		profiles, err := connectionProfiles(value)
		if err != nil {
			return "", err
		}
		profile, err := selectProfile(requested, currentProfile, profiles)
		if err != nil {
			return "", err
		}
		if state != mbn.MBN_ACTIVATION_STATE_DEACTIVATED && state != mbn.MBN_ACTIVATION_STATE_NONE {
			var requestID uint32
			if state != mbn.MBN_ACTIVATION_STATE_DEACTIVATING {
				if err := connection.Disconnect(&requestID); err != nil {
					return "", fmt.Errorf("disconnect pre-existing MBN data: %w", err)
				}
			}
			if err := waitConnection(ctx, connection, mbn.MBN_ACTIVATION_STATE_DEACTIVATED); err != nil {
				return "", err
			}
		}
		var requestID uint32
		if err := connection.Connect(mbn.MBN_CONNECTION_MODE_PROFILE, profile, &requestID); err != nil {
			return "", fmt.Errorf("connect MBN profile %q: %w", profile, err)
		}
		if err := waitConnection(ctx, connection, mbn.MBN_ACTIVATION_STATE_ACTIVATED); err != nil {
			return "", err
		}
		return profile, nil
	})
}

func disconnectData(ctx context.Context, attachmentID string) error {
	_, err := withMBNInterface(ctx, attachmentID, func(value *mbn.IMbnInterface) (string, error) {
		connection, err := value.GetConnection()
		if err != nil || connection == nil {
			if connection != nil {
				connection.Release()
			}
			return "", err
		}
		defer connection.Release()
		var state mbn.MBN_ACTIVATION_STATE
		var profile foundation.BSTR
		if err := connection.GetConnectionState(&state, &profile); err != nil {
			return "", err
		}
		takeBSTR(profile)
		if state == mbn.MBN_ACTIVATION_STATE_DEACTIVATED || state == mbn.MBN_ACTIVATION_STATE_NONE {
			return "", nil
		}
		var requestID uint32
		if state != mbn.MBN_ACTIVATION_STATE_DEACTIVATING {
			if err := connection.Disconnect(&requestID); err != nil {
				return "", err
			}
		}
		return "", waitConnection(ctx, connection, mbn.MBN_ACTIVATION_STATE_DEACTIVATED)
	})
	return err
}

func withMBNInterface(ctx context.Context, attachmentID string, operation func(*mbn.IMbnInterface) (string, error)) (string, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if _, err := com.CoInitializeEx(uint32(com.COINIT_MULTITHREADED)); err != nil {
		return "", err
	}
	defer com.CoUninitialize()
	var root *win32.IUnknown
	if err := com.CoCreateInstance(&clsidMbnInterfaceManager, nil, com.CLSCTX_INPROC_SERVER, &mbn.IID_IMbnInterfaceManager, &root); err != nil {
		if root != nil {
			root.Release()
		}
		return "", err
	}
	manager := win32.Cast[mbn.IMbnInterfaceManager](root)
	defer manager.Release()
	var interfaces *com.SAFEARRAY
	if err := manager.GetInterfaces(&interfaces); err != nil {
		if interfaces != nil {
			ole.SafeArrayDestroy(interfaces)
		}
		return "", err
	}
	if interfaces == nil {
		return "", errors.New("MBN interface is unavailable")
	}
	defer ole.SafeArrayDestroy(interfaces)
	lower, upper, err := bounds(interfaces)
	if err != nil {
		return "", err
	}
	want := normalizeAttachmentID(attachmentID)
	for index := lower; index <= upper; index++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		var current *mbn.IMbnInterface
		if err := ole.SafeArrayGetElement(interfaces, &index, unsafe.Pointer(&current)); err != nil {
			return "", err
		}
		if current == nil {
			continue
		}
		id := normalizeAttachmentID(readBSTR(current.Get_InterfaceID))
		if id == want {
			result, operationErr := operation(current)
			current.Release()
			return result, operationErr
		}
		current.Release()
	}
	return "", agentmodem.ErrOperationTargetReplaced
}

func connectionProfiles(value *mbn.IMbnInterface) ([]mbnProfileXML, error) {
	var root *win32.IUnknown
	if err := com.CoCreateInstance(&clsidMbnConnectionProfileManager, nil, com.CLSCTX_INPROC_SERVER, &mbn.IID_IMbnConnectionProfileManager, &root); err != nil {
		if root != nil {
			root.Release()
		}
		return nil, err
	}
	manager := win32.Cast[mbn.IMbnConnectionProfileManager](root)
	defer manager.Release()
	var array *com.SAFEARRAY
	if err := manager.GetConnectionProfiles(value, &array); err != nil {
		if array != nil {
			ole.SafeArrayDestroy(array)
		}
		return nil, err
	}
	if array == nil {
		return nil, errors.New("no MBN connection profile is configured")
	}
	defer ole.SafeArrayDestroy(array)
	lower, upper, err := bounds(array)
	if err != nil {
		return nil, err
	}
	profiles := make([]mbnProfileXML, 0, int(upper-lower+1))
	for index := lower; index <= upper; index++ {
		var profile *mbn.IMbnConnectionProfile
		if err := ole.SafeArrayGetElement(array, &index, unsafe.Pointer(&profile)); err != nil {
			return nil, err
		}
		if profile == nil {
			continue
		}
		var data foundation.BSTR
		getErr := profile.GetProfileXmlData(&data)
		profile.Release()
		if getErr != nil {
			return nil, getErr
		}
		var parsed mbnProfileXML
		if err := xml.Unmarshal([]byte(takeBSTR(data)), &parsed); err != nil || strings.TrimSpace(parsed.Name) == "" {
			return nil, errors.New("invalid MBN profile XML")
		}
		parsed.Name = strings.TrimSpace(parsed.Name)
		profiles = append(profiles, parsed)
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].Name < profiles[j].Name })
	return profiles, nil
}

func selectProfile(requested, current string, profiles []mbnProfileXML) (string, error) {
	requested, current = strings.TrimSpace(requested), strings.TrimSpace(current)
	if requested != "" {
		for _, profile := range profiles {
			if profile.Name == requested {
				return requested, nil
			}
		}
		return "", errors.New("requested MBN profile was not found")
	}
	if current != "" {
		for _, profile := range profiles {
			if profile.Name == current {
				return current, nil
			}
		}
	}
	for _, profile := range profiles {
		if profile.IsDefault {
			return profile.Name, nil
		}
	}
	if len(profiles) == 1 {
		return profiles[0].Name, nil
	}
	return "", errors.New("multiple MBN profiles are available; an explicit profile is required")
}

func waitConnection(ctx context.Context, connection *mbn.IMbnConnection, wanted mbn.MBN_ACTIVATION_STATE) error {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		var state mbn.MBN_ACTIVATION_STATE
		var profile foundation.BSTR
		if err := connection.GetConnectionState(&state, &profile); err != nil {
			return err
		}
		takeBSTR(profile)
		if state == wanted {
			return nil
		}
		if wanted == mbn.MBN_ACTIVATION_STATE_ACTIVATED && state == mbn.MBN_ACTIVATION_STATE_DEACTIVATED { /* async request may not have started yet */
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func interfaceSources(value *net.Interface) (map[bool]netip.Addr, error) {
	addresses, err := value.Addrs()
	if err != nil {
		return nil, err
	}
	result := map[bool]netip.Addr{}
	for _, raw := range addresses {
		prefix, err := netip.ParsePrefix(raw.String())
		if err != nil {
			continue
		}
		address := prefix.Addr().Unmap()
		if address.IsValid() && !address.IsUnspecified() && !address.IsLoopback() {
			if _, exists := result[address.Is6()]; !exists {
				result[address.Is6()] = address
			}
		}
	}
	return result, nil
}

func resolveDataDestination(ctx context.Context, host string) ([]netip.Addr, error) {
	if address, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		return []netip.Addr{address.Unmap()}, nil
	}
	addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	result := make([]netip.Addr, 0, len(addresses))
	for _, address := range addresses {
		if address.IsValid() && !address.IsUnspecified() {
			result = append(result, address.Unmap())
		}
	}
	if len(result) == 0 {
		return nil, errors.New("destination resolved to no usable address")
	}
	return result, nil
}

func localAddress(network string, address netip.Addr, zone string) net.Addr {
	ip := net.IP(address.AsSlice())
	if network == "udp" {
		return &net.UDPAddr{IP: ip, Zone: zone}
	}
	return &net.TCPAddr{IP: ip, Zone: zone}
}

func bindInterfaceControl(index uint32, ipv6 bool) func(string, string, syscall.RawConn) error {
	return func(_, _ string, raw syscall.RawConn) error {
		var optionErr error
		err := raw.Control(func(fd uintptr) {
			level, value := windows.IPPROTO_IP, int(reverseBytes32(index))
			if ipv6 {
				level, value = windows.IPPROTO_IPV6, int(index)
			}
			optionErr = windows.SetsockoptInt(windows.Handle(fd), level, socketOptionUnicastInterface, value)
		})
		return errors.Join(err, optionErr)
	}
}

func reverseBytes32(value uint32) uint32 {
	return value>>24 | value>>8&0x0000ff00 | value<<8&0x00ff0000 | value<<24
}
