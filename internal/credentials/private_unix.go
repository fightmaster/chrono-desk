//go:build !windows

package credentials

import (
	"errors"
	"os"
)

func checkPrivatePath(path string, directory bool) error {
	info, err := os.Lstat(path)
	if err != nil || (directory && !info.IsDir()) || (!directory && !info.Mode().IsRegular()) || info.Mode().Perm()&0077 != 0 {
		return errors.New("TLS: каталог и ключ должны быть доступны только владельцу (0700/0600), без символических ссылок")
	}
	return nil
}

func securePrivatePath(path string, directory bool) error {
	mode := os.FileMode(0o600)
	if directory {
		mode = 0o700
	}
	return os.Chmod(path, mode)
}
