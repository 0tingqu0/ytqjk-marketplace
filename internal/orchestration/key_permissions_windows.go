//go:build windows

package orchestration

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

const windowsFileFullControl = windows.ACCESS_MASK(windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1ff)

func secureKeyPermissions(path string, created bool) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil {
		return errors.New("identity key owner is unavailable")
	}
	userSID := user.User.Sid.String()
	if created {
		descriptor, err := windows.SecurityDescriptorFromString(fmt.Sprintf("D:P(A;;FA;;;%s)(A;;FA;;;SY)(A;;FA;;;BA)", userSID))
		if err != nil {
			return errors.New("identity key ACL could not be created")
		}
		dacl, _, err := descriptor.DACL()
		if err != nil || dacl == nil {
			return errors.New("identity key ACL could not be created")
		}
		information := windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION)
		if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, information, nil, nil, dacl, nil); err != nil {
			return errors.New("identity key ACL could not be restricted")
		}
	}
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil || descriptor == nil {
		return errors.New("identity key ACL is unavailable")
	}
	control, _, err := descriptor.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return errors.New("identity key ACL inherits permissions")
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil || dacl.AceCount != 3 {
		return errors.New("identity key ACL is not least privilege")
	}
	allowed := map[string]bool{userSID: true, "S-1-5-18": true, "S-1-5-32-544": true}
	seen := map[string]bool{}
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil || ace == nil {
			return errors.New("identity key ACL is unreadable")
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags&windows.INHERITED_ACE != 0 || ace.Mask != windowsFileFullControl {
			return errors.New("identity key ACL contains unsafe rights")
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		value := sid.String()
		if !allowed[value] || seen[value] {
			return errors.New("identity key ACL contains an unexpected identity")
		}
		seen[value] = true
	}
	if len(seen) != len(allowed) {
		return errors.New("identity key ACL is incomplete")
	}
	return nil
}
