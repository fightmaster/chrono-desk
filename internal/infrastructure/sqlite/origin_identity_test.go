package sqlite

import "testing"

func TestInstallationOriginSurvivesRestartAndSequenceNeverReuses(t *testing.T) {
	dir := t.TempDir()
	first, err := loadOrCreateInstallationOrigin(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if len(first.instanceID) != 36 {
		t.Fatalf("instance id = %q, want UUID", first.instanceID)
	}
	sequence, err := first.Next()
	if err != nil {
		t.Fatal(err)
	}
	if sequence != 1 {
		t.Fatalf("first sequence = %d, want 1", sequence)
	}

	restarted, err := loadOrCreateInstallationOrigin(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if restarted.instanceID != first.instanceID {
		t.Fatalf("instance changed after restart: %q != %q", restarted.instanceID, first.instanceID)
	}
	sequence, err = restarted.Next()
	if err != nil {
		t.Fatal(err)
	}
	if sequence != 2 {
		t.Fatalf("sequence after restart = %d, want 2", sequence)
	}
}
