//go:build windows

package main

import (
	"bytes"
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

var windowsSecurity = syscall.NewLazyDLL("advapi32.dll")
var convertSecurityDescriptor = windowsSecurity.NewProc("ConvertStringSecurityDescriptorToSecurityDescriptorW")
var securityDescriptorDACL = windowsSecurity.NewProc("GetSecurityDescriptorDacl")
var getSecurityInfo = windowsSecurity.NewProc("GetSecurityInfo")

// Windows mode bits expose readonly, not access control. Create new provider files
// with a protected DACL for this user, SYSTEM and administrators, then verify it on
// the open handle. No existing directory, provider state or credential ACL is changed.
func createPrivateProviderFile(path string) (*os.File, error) {
	token, err := syscall.OpenCurrentProcessToken()
	if err != nil {
		return nil, err
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return nil, err
	}
	sid, err := user.User.Sid.String()
	if err != nil {
		return nil, err
	}
	sddl, err := syscall.UTF16PtrFromString("O:" + sid + "D:P(A;;FA;;;" + sid + ")(A;;FA;;;SY)(A;;FA;;;BA)")
	if err != nil {
		return nil, err
	}
	var descriptor unsafe.Pointer
	ok, _, callErr := convertSecurityDescriptor.Call(uintptr(unsafe.Pointer(sddl)), 1, uintptr(unsafe.Pointer(&descriptor)), 0)
	if ok == 0 {
		return nil, fmt.Errorf("private probe security descriptor: %w", callErr)
	}
	defer syscall.LocalFree(syscall.Handle(uintptr(descriptor)))
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	sa := syscall.SecurityAttributes{Length: uint32(unsafe.Sizeof(syscall.SecurityAttributes{})), SecurityDescriptor: uintptr(descriptor)}
	h, err := syscall.CreateFile(name, syscall.GENERIC_READ|syscall.GENERIC_WRITE|0x20000, syscall.FILE_SHARE_READ, &sa, syscall.CREATE_NEW, syscall.FILE_ATTRIBUTE_NORMAL|syscall.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(h), path)
	if err = verifyPrivateProbe(h, descriptor, sid); err != nil {
		f.Close()
		os.Remove(path)
		return nil, err
	}
	return f, nil
}

func descriptorACL(descriptor unsafe.Pointer) ([]byte, error) {
	var present, defaulted uint32
	var acl *byte
	ok, _, err := securityDescriptorDACL.Call(uintptr(descriptor), uintptr(unsafe.Pointer(&present)), uintptr(unsafe.Pointer(&acl)), uintptr(unsafe.Pointer(&defaulted)))
	if ok == 0 || present == 0 || acl == nil {
		return nil, fmt.Errorf("probe DACL missing or unreadable: %v", err)
	}
	// ACL header: revision, reserved, uint16 size. Windows validated this descriptor.
	size := *(*uint16)(unsafe.Add(unsafe.Pointer(acl), 2))
	if size < 8 {
		return nil, fmt.Errorf("invalid probe DACL size")
	}
	return unsafe.Slice(acl, int(size)), nil
}

func verifyPrivateProbe(h syscall.Handle, expected unsafe.Pointer, userSID string) error {
	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(h, &info); err != nil {
		return err
	}
	if info.FileAttributes&(syscall.FILE_ATTRIBUTE_REPARSE_POINT|syscall.FILE_ATTRIBUTE_DIRECTORY) != 0 || info.NumberOfLinks != 1 {
		return fmt.Errorf("probe is linked or not a regular file")
	}
	var actual unsafe.Pointer
	var owner *syscall.SID
	code, _, _ := getSecurityInfo.Call(uintptr(h), 1, 5, uintptr(unsafe.Pointer(&owner)), 0, 0, 0, uintptr(unsafe.Pointer(&actual))) // SE_FILE_OBJECT, OWNER|DACL
	if code != 0 {
		return fmt.Errorf("read probe security: %w", syscall.Errno(code))
	}
	defer syscall.LocalFree(syscall.Handle(uintptr(actual)))
	if owner == nil {
		return fmt.Errorf("probe owner missing")
	}
	gotOwner, err := owner.String()
	if err != nil || gotOwner != userSID {
		return fmt.Errorf("probe owner mismatch")
	}
	want, err := descriptorACL(expected)
	if err != nil {
		return err
	}
	got, err := descriptorACL(actual)
	if err != nil {
		return err
	}
	if !bytes.Equal(want, got) {
		return fmt.Errorf("probe DACL mismatch")
	}
	return nil
}

func lifecycleProbeModeValid(info os.FileInfo) bool { return info.Mode().Perm() == 0666 }
