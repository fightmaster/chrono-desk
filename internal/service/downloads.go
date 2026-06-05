package service

import (
	"fmt"
	"os"
	"path/filepath"
)

// DownloadsDir resolves the user's Downloads directory, falling back to the
// home directory, then CWD.
func DownloadsDir() (string, error) {
	dir := "."
	if home, err := os.UserHomeDir(); err == nil {
		dir = home
		if downloads := filepath.Join(home, "Downloads"); isDir(downloads) {
			dir = downloads
		}
	}
	return dir, nil
}

// SaveToDownloads writes data into the user's Downloads directory and returns
// the absolute path.
func SaveToDownloads(name string, data []byte) (string, error) {
	dir, err := DownloadsDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", fmt.Errorf("save %s: %w", path, err)
	}
	return path, nil
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
