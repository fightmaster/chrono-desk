package sqlite

import (
	"os"
	"testing"
)

func TestEventStorageStatsReportsMainWALAndSHMWithoutPaths(t *testing.T) {
	catalog, err := NewEventCatalog(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = catalog.Close() })
	path := catalog.eventPath("ev/unsafe")
	for file, content := range map[string][]byte{
		path:          []byte("database"),
		path + "-wal": []byte("wal"),
		path + "-shm": []byte("shm-data"),
	} {
		if err := os.WriteFile(file, content, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	stats, err := catalog.StorageStats("ev/unsafe")
	if err != nil {
		t.Fatal(err)
	}
	if stats.DatabaseBytes != 8 || stats.WALBytes != 3 || stats.SHMBytes != 8 || stats.TotalBytes != 19 {
		t.Fatalf("storage stats=%+v", stats)
	}
}

func TestEventStorageStatsAllowsAbsentWALAndSHM(t *testing.T) {
	catalog, err := NewEventCatalog(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = catalog.Close() })
	if err := os.WriteFile(catalog.eventPath("ev-1"), []byte("database"), 0o600); err != nil {
		t.Fatal(err)
	}

	stats, err := catalog.StorageStats("ev-1")
	if err != nil {
		t.Fatal(err)
	}
	if stats.DatabaseBytes != 8 || stats.WALBytes != 0 || stats.SHMBytes != 0 || stats.TotalBytes != 8 {
		t.Fatalf("storage stats=%+v", stats)
	}
}
