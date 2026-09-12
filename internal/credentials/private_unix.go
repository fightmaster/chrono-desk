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
