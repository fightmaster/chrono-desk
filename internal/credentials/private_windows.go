//go:build windows

package credentials

import (
	"errors"
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows mode bits do not describe the ACL. Require current-user ownership,
// and permit ordinary allow ACEs only for that user, SYSTEM or Administrators.
// Unknown/conditional/object ACEs fail closed instead of guessing their scope.
func checkPrivatePath(path string, directory bool) error {
	failure := errors.New("TLS: каталог и ключ должны иметь закрытый ACL текущего пользователя Windows")
	info, err := os.Lstat(path)
	if err != nil || (directory && !info.IsDir()) || (!directory && !info.Mode().IsRegular()) || info.Mode()&os.ModeSymlink != 0 {
		return failure
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return failure
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return failure
	}
	defer runtime.KeepAlive(sd)
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !owner.Equals(user.User.Sid) {
		return failure
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil {
		return failure
	}
	for n := uint32(0); n < uint32(acl.AceCount); n++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, n, &ace); err != nil || ace == nil {
			return failure
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceSize < uint16(unsafe.Sizeof(*ace)) {
			return failure
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !sid.IsValid() || (!sid.Equals(user.User.Sid) && !sid.IsWellKnown(windows.WinLocalSystemSid) && !sid.IsWellKnown(windows.WinBuiltinAdministratorsSid)) {
			return failure
		}
	}
	return nil
}
