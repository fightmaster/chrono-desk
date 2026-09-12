//go:build windows

package credentials

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsPrivateCredentialsValidateActualACL(t *testing.T) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	key := filepath.Join(directory, "client-key.pem")
	if err := os.WriteFile(key, []byte("synthetic-not-a-real-key"), 0600); err != nil {
		t.Fatal(err)
	}
	set := func(path, suffix string) {
		t.Helper()
		sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + user.User.Sid.String() + ")(A;;FA;;;SY)(A;;FA;;;BA)" + suffix)
		if err != nil {
			t.Fatal(err)
		}
		acl, _, err := sd.DACL()
		if err != nil {
			t.Fatal(err)
		}
		if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{directory, key} {
		set(path, "")
		if err := checkPrivatePath(path, path == directory); err != nil {
			t.Fatalf("private owner ACL rejected: %v", err)
		}
		set(path, "(A;;FR;;;WD)")
		if err := checkPrivatePath(path, path == directory); err == nil {
			t.Fatal("world-readable Windows ACL accepted as private")
		}
		set(path, "")
	}
}
