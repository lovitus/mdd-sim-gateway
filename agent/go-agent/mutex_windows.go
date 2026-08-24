//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

var (
	kernel32CreateMutex = syscall.NewLazyDLL("kernel32.dll").NewProc("CreateMutexW")
	kernel32CloseHandle = syscall.NewLazyDLL("kernel32.dll").NewProc("CloseHandle")
	unifiedMutexHandle  uintptr
)

func acquireUnifiedAgentLease() bool {
	name, err := syscall.UTF16PtrFromString(`Global\MDDUnifiedAgent-v1`)
	if err != nil {
		return false
	}
	handle, _, callErr := kernel32CreateMutex.Call(0, 0, uintptr(unsafe.Pointer(name)))
	if handle == 0 {
		return false
	}
	if errno, ok := callErr.(syscall.Errno); ok && errno == syscall.ERROR_ALREADY_EXISTS {
		kernel32CloseHandle.Call(handle)
		return false
	}
	unifiedMutexHandle = handle
	return true
}
