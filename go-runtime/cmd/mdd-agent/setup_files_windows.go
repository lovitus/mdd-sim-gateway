package main

import (
	"errors"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

func editedConfigSecurity(original string) (*windows.SECURITY_DESCRIPTOR, error) {
	sd, err := windows.GetNamedSecurityInfo(original, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.GROUP_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return nil, err
	}
	if sd == nil {
		return nil, errors.New("existing configuration has no security descriptor")
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return nil, err
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return nil, err
	}
	if owner == nil || acl == nil {
		return nil, errors.New("existing configuration has no private owner or access list")
	}
	if err := sd.SetControl(windows.SE_DACL_PROTECTED, windows.SE_DACL_PROTECTED); err != nil {
		return nil, err
	}
	return sd, nil
}

func setupSecurity() (*windows.SECURITY_DESCRIPTOR, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	return windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;" + user.User.Sid.String() + ")")
}

// Install the descriptor in CREATE_NEW itself. Tightening an inherited ACL
// after creation cannot revoke a read handle already obtained by another user.
func createSetupTemp(directory, original string) (*os.File, error) {
	var sd *windows.SECURITY_DESCRIPTOR
	var err error
	if original != "" {
		sd, err = editedConfigSecurity(original)
	} else {
		sd, err = setupSecurity()
	}
	if err != nil {
		return nil, err
	}
	nonce, err := randomHex(16)
	if err != nil {
		return nil, err
	}
	name := filepath.Join(directory, ".mdd-setup-"+nonce)
	path, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	attributes := windows.SecurityAttributes{SecurityDescriptor: sd}
	attributes.Length = uint32(unsafe.Sizeof(attributes))
	handle, err := windows.CreateFile(path, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		&attributes, windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(handle), name), nil
}

func protectSetupPath(path string) error {
	sd, err := setupSecurity()
	if err != nil {
		return err
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}

func lockSetupFile(file *os.File) error {
	return windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &windows.Overlapped{})
}
