//go:build windows

package windowspnp

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	cfgmgr32          = windows.NewLazySystemDLL("cfgmgr32.dll")
	procCMGetParent   = cfgmgr32.NewProc("CM_Get_Parent")
	procCMGetIDSize   = cfgmgr32.NewProc("CM_Get_Device_ID_Size")
	procCMGetDeviceID = cfgmgr32.NewProc("CM_Get_Device_IDW")
)

func Ports() ([]Port, error) {
	guids, err := windows.SetupDiClassGuidsFromNameEx("Ports", "")
	if err != nil {
		return nil, err
	}
	result := []Port{}
	for _, guid := range guids {
		set, err := windows.SetupDiGetClassDevsEx(&guid, "", 0, windows.DIGCF_PRESENT, 0, "")
		if err != nil {
			return nil, err
		}
		for index := 0; ; index++ {
			data, enumErr := windows.SetupDiEnumDeviceInfo(set, index)
			if errors.Is(enumErr, windows.ERROR_NO_MORE_ITEMS) {
				break
			}
			if enumErr != nil {
				_ = windows.SetupDiDestroyDeviceInfoList(set)
				return nil, enumErr
			}
			name, nameErr := portName(set, data)
			if nameErr != nil || !validCOM(name) {
				continue
			}
			instanceID, instanceErr := windows.SetupDiGetDeviceInstanceId(set, data)
			physicalID, parentErr := parentInstanceID(data.DevInst)
			if instanceErr != nil || parentErr != nil {
				continue
			}
			product := ""
			if value, propertyErr := windows.SetupDiGetDeviceRegistryProperty(set, data, windows.SPDRP_FRIENDLYNAME); propertyErr == nil {
				product, _ = value.(string)
			}
			result = append(result, Port{
				Name: strings.ToUpper(name), Product: strings.TrimSpace(product),
				InstanceID: strings.ToUpper(instanceID), PhysicalID: strings.ToUpper(physicalID),
				USB: strings.HasPrefix(strings.ToUpper(instanceID), "USB\\"),
			})
		}
		if err := windows.SetupDiDestroyDeviceInfoList(set); err != nil {
			return nil, err
		}
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Name < result[right].Name })
	return result, nil
}

func portName(set windows.DevInfo, data *windows.DevInfoData) (string, error) {
	handle, err := windows.SetupDiOpenDevRegKey(set, data, windows.DICS_FLAG_GLOBAL, 0, windows.DIREG_DEV, windows.KEY_READ)
	if err != nil {
		return "", err
	}
	key := registry.Key(handle)
	defer key.Close()
	value, _, err := key.GetStringValue("PortName")
	return strings.TrimSpace(value), err
}

func parentInstanceID(device windows.DEVINST) (string, error) {
	var parent windows.DEVINST
	status, _, _ := procCMGetParent.Call(uintptr(unsafe.Pointer(&parent)), uintptr(device), 0)
	if status != 0 {
		return "", fmt.Errorf("CM_Get_Parent failed: CR %d", status)
	}
	var size uint32
	status, _, _ = procCMGetIDSize.Call(uintptr(unsafe.Pointer(&size)), uintptr(parent), 0)
	if status != 0 || size == 0 || size > 4096 {
		return "", fmt.Errorf("CM_Get_Device_ID_Size failed: CR %d", status)
	}
	buffer := make([]uint16, size+1)
	status, _, _ = procCMGetDeviceID.Call(uintptr(parent), uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)), 0)
	if status != 0 {
		return "", fmt.Errorf("CM_Get_Device_ID failed: CR %d", status)
	}
	return windows.UTF16ToString(buffer), nil
}

func validCOM(value string) bool {
	upper := strings.ToUpper(value)
	if !strings.HasPrefix(upper, "COM") || len(upper) < 4 {
		return false
	}
	for _, character := range upper[3:] {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}
