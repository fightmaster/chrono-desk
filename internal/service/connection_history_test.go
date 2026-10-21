package service

import (
	"io"
	"log"
	"strings"
	"testing"

	"gitlab.com/fightmaster1/rfid-core/diagnostics"
)

func TestLiveHistoryOptionalAndSurvivesSessionAbsence(t *testing.T) {
	without := NewLiveManager(log.New(io.Discard, "", 0))
	if _, err := without.ExportConnectionHistory(); err == nil {
		t.Fatal("missing history accepted")
	}
	h, err := diagnostics.New(t.TempDir(), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	live := NewLiveManager(log.New(io.Discard, "", 0), WithConnectionHistory(h))
	if live.Status("unused").ConnectionHistory == nil {
		t.Fatal("history health not exposed")
	}
	h.Record(diagnostics.Event{State: "disconnected"})
	h.Close()
	b, err := live.ExportConnectionHistory()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "disconnected") {
		t.Fatal(string(b))
	}
}
