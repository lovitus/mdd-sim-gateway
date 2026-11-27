//go:build windows

package pcscmonitor

import (
	"errors"

	"github.com/ebfe/scard"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// Windows shuts down SCardSvr automatically when zero smart card readers are
// present, returning SCARD_E_NO_SERVICE or SCARD_E_SERVICE_STOPPED instead of
// SCARD_E_NO_READERS_AVAILABLE. Only treat that state as an empty reader set
// when SCardSvr is not disabled and PnP confirms zero present reader devices.
var (
	smartCardReaderClassGUID       = windows.GUID{Data1: 0x50dd5230, Data2: 0xba8a, Data3: 0x11d1, Data4: [8]byte{0xbf, 0x5d, 0x00, 0x00, 0xf8, 0x05, 0xf5, 0x30}}
	smartCardTriggerInterfaceGUIDs = []windows.GUID{
		{Data1: 0x50dd5230, Data2: 0xba8a, Data3: 0x11d1, Data4: [8]byte{0xbf, 0x5d, 0x00, 0x00, 0xf8, 0x05, 0xf5, 0x30}},
		{Data1: 0x121d8161, Data2: 0x866d, Data3: 0x4a24, Data4: [8]byte{0xba, 0x58, 0x90, 0x58, 0x94, 0x0c, 0x0d, 0x47}},
	}
)

func allowIdleNoReaders(err error) bool {
	return allowIdleNoReadersWith(err, querySmartCardServiceState, querySmartCardReaderPresent)
}

func allowIdleNoReadersWith(
	err error,
	queryService func() (svc.State, uint32, error),
	queryReaderPresent func() (bool, error),
) bool {
	if !errors.Is(err, scard.ErrNoService) && !errors.Is(err, scard.ErrServiceStopped) {
		return false
	}
	state, startType, svcErr := queryService()
	if svcErr != nil || startType == windows.SERVICE_DISABLED || (state != svc.Stopped && state != svc.StopPending) {
		return false
	}
	present, pnpErr := queryReaderPresent()
	if pnpErr != nil || present {
		return false
	}
	return true
}

func querySmartCardServiceState() (svc.State, uint32, error) {
	scm, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return 0, 0, err
	}
	defer windows.CloseServiceHandle(scm)
	name, err := windows.UTF16PtrFromString("SCardSvr")
	if err != nil {
		return 0, 0, err
	}
	handle, err := windows.OpenService(scm, name, windows.SERVICE_QUERY_STATUS|windows.SERVICE_QUERY_CONFIG)
	if err != nil {
		return 0, 0, err
	}
	service := &mgr.Service{Name: "SCardSvr", Handle: handle}
	defer service.Close()
	status, err := service.Query()
	if err != nil {
		return 0, 0, err
	}
	cfg, err := service.Config()
	if err != nil {
		return 0, 0, err
	}
	return status.State, cfg.StartType, nil
}

func querySmartCardReaderPresent() (bool, error) {
	classGUID := smartCardReaderClassGUID
	if present, err := hasPresentSmartCardDevices(&classGUID, windows.DIGCF_PRESENT); err != nil || present {
		return present, err
	}
	for index := range smartCardTriggerInterfaceGUIDs {
		guid := smartCardTriggerInterfaceGUIDs[index]
		if present, err := hasPresentSmartCardDevices(&guid, windows.DIGCF_PRESENT|windows.DIGCF_DEVICEINTERFACE); err != nil || present {
			return present, err
		}
	}
	return false, nil
}

func hasPresentSmartCardDevices(guid *windows.GUID, flags windows.DIGCF) (bool, error) {
	set, err := windows.SetupDiGetClassDevsEx(guid, "", 0, flags, 0, "")
	if err != nil {
		return false, err
	}
	defer windows.SetupDiDestroyDeviceInfoList(set)
	_, err = windows.SetupDiEnumDeviceInfo(set, 0)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, windows.ERROR_NO_MORE_ITEMS) {
		return false, nil
	}
	return false, err
}
