package publicweb

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
	"gitlab.com/fightmaster1/chrono-desk/internal/service"
	"gitlab.com/fightmaster1/rfid-core/edge"
	"gitlab.com/fightmaster1/rfid-core/ingest"
)

type liveFinishFixture struct {
	server   *Server
	store    *sqlite.Store
	manager  *service.LiveManager
	conn     net.Conn
	reader   *bufio.Reader
	sequence int64
	baseTime int64
}

func newLiveFinishFixture(t *testing.T, members int) *liveFinishFixture {
	t.Helper()
	s := newServer(t)
	data, err := os.ReadFile("../../service/testdata/event-export.json")
	if err != nil {
		t.Fatal(err)
	}
	data = bytes.ReplaceAll(data, []byte("ev-100"), []byte("100"))
	if _, err = s.events.ImportExport(context.Background(), bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	store, err := s.events.Open("100")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	start := time.Now().Add(-30 * time.Minute).UnixMilli()
	if err := store.UpsertRace(ctx, domain.Race{ID: "race-10k", EventID: "100", Name: "Synthetic", Format: domain.FormatFixedDistance, StartedAtMs: &start}); err != nil {
		t.Fatal(err)
	}
	if err := store.WithinTx(ctx, func(tx *sqlite.Store) error {
		for i := 1; i <= members; i++ {
			epc := fmt.Sprintf("E280%08X", i)
			number := int64(1000 + i)
			if err := tx.UpsertMember(ctx, domain.Member{ID: fmt.Sprintf("live-%04d", i), EventID: "100", RaceID: "race-10k", EPC: &epc, Number: &number, FirstName: "Synthetic", LastName: "Runner"}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	manager := service.NewLiveManager(log.New(io.Discard, "", 0))
	t.Cleanup(manager.StopAll)
	if err := manager.ConfigureEdge(ctx, store, "100", []domain.EdgeBinding{{Board: "Feibot:U659", SourceSessionID: "100"}}); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	listener.Close()
	if err := manager.StartCombined(store, "100", port); err != nil {
		t.Fatal(err)
	}
	var conn net.Conn
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		conn, err = net.DialTimeout("tcp", "127.0.0.1:"+port, 50*time.Millisecond)
		if err == nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	s.publishedID = "100"
	return &liveFinishFixture{server: s, store: store, manager: manager, conn: conn, reader: bufio.NewReader(conn), baseTime: time.Now().UnixMilli()}
}

func (f *liveFinishFixture) frame(member int, sidecar bool) ([]byte, string) {
	f.sequence++
	epc := "FFFFFFFF"
	if member > 0 {
		epc = fmt.Sprintf("E280%08X", member)
	}
	ms := f.baseTime + f.sequence
	rtc := time.UnixMilli(ms).UTC().Format(time.RFC3339Nano)
	if !sidecar {
		return []byte(fmt.Sprintf(`{"DeviceCode":"U659","time":%q,"epc":%q,"channelId":1,"rssi":-50}`, rtc, epc)), "ok\n"
	}
	event := ingest.Event{EdgeVersion: 1, ExternalEventID: 100, SourceSessionID: "100", IdentityProfile: edge.IdentityRFID, ClockEvidenceID: "synthetic-clock", ClockQuality: edge.ClockSource,
		Board: "Feibot:U659", EPC: epc, Time: ms, RTC: rtc, Ant: 1, RSSI: -50, ObservationVersion: 1, CaptureSourceID: "edge:U659:100", OriginSystem: "feibot-sidecar", OriginInstanceID: "synthetic-sidecar", OriginSequence: uint64(f.sequence)}
	event.ID = ingest.RFIDReadID(event.Board, event.EPC, event.Time, event.Ant)
	payload, err := edge.Encode(event)
	if err != nil {
		panic(err)
	}
	return payload, string(edge.ACK(event))
}

func (f *liveFinishFixture) exchange(payload []byte, wantACK string) error {
	if err := f.conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		return err
	}
	if _, err := f.conn.Write(append(append([]byte(nil), payload...), '\n')); err != nil {
		return err
	}
	ack, err := f.reader.ReadString('\n')
	if err != nil {
		return err
	}
	if ack != wantACK {
		return fmt.Errorf("ACK=%q expected=%q", ack, wantACK)
	}
	return nil
}

func TestFinishStreamRealNativeAndSidecarACKWhileWriterBlocked(t *testing.T) {
	f := newLiveFinishFixture(t, 3)
	s := f.server
	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	a := openTestFinishStream(t, ts.URL+"/api/live/finishes/stream")
	defer a.close()
	a.next(t)
	b := openTestFinishStream(t, ts.URL+"/api/live/finishes/stream")
	defer b.close()
	b.next(t)
	w := &blockedFinishWriter{header: make(http.Header), initial: make(chan struct{}), blocked: make(chan struct{}), release: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.handleFinishStream(w, httptest.NewRequest(http.MethodGet, "/api/live/finishes/stream", nil).WithContext(ctx))
	}()
	defer func() {
		cancel()
		close(w.release)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("slow handler leaked")
		}
	}()
	select {
	case <-w.initial:
	case <-time.After(time.Second):
		t.Fatal("no slow-client initial snapshot")
	}
	var rawBefore int
	if err := f.store.DB().QueryRow(`SELECT COUNT(*) FROM rfid_logs`).Scan(&rawBefore); err != nil {
		t.Fatal(err)
	}
	native, nativeACK := f.frame(1, false)
	if err := f.exchange(native, nativeACK); err != nil {
		t.Fatal(err)
	}
	select {
	case <-w.blocked:
	case <-time.After(time.Second):
		t.Fatal("slow writer never blocked")
	}
	for _, client := range []*testFinishStream{a, b} {
		_, data := client.next(t)
		if *data.Rows[0].Number != 1001 {
			t.Fatal(data)
		}
	}
	sidecar, sidecarACK := f.frame(2, true)
	acknowledged := make(chan error, 1)
	go func() { acknowledged <- f.exchange(sidecar, sidecarACK) }()
	select {
	case err := <-acknowledged:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("slow SSE writer blocked sidecar commit/ACK")
	}
	select {
	case <-done:
		t.Fatal("blocked writer was already released before ACK proof")
	default:
	}
	for _, client := range []*testFinishStream{a, b} {
		_, data := client.next(t)
		if *data.Rows[0].Number != 1002 {
			t.Fatal(data)
		}
	}
	generation := f.store.FinishChanges().Generation()
	for i := 0; i < 100; i++ {
		payload, ack := f.frame(0, i%2 == 0)
		if err := f.exchange(payload, ack); err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range []struct {
		frame []byte
		ack   string
	}{{native, nativeACK}, {sidecar, sidecarACK}} {
		if err := f.exchange(item.frame, item.ack); err != nil {
			t.Fatal(err)
		}
	}
	if f.store.FinishChanges().Generation() != generation {
		t.Fatal("unmatched/duplicate traffic emitted finish hints")
	}
	var raw, nativeOwned, edgeOwned int
	if err := f.store.DB().QueryRow(`SELECT (SELECT COUNT(*) FROM rfid_logs),(SELECT COUNT(*) FROM observation_outbox),(SELECT COUNT(*) FROM edge_observation_outbox)`).Scan(&raw, &nativeOwned, &edgeOwned); err != nil {
		t.Fatal(err)
	}
	if raw-rawBefore != 102 || nativeOwned != 51 || edgeOwned != 51 {
		t.Fatalf("raw delta=%d native ownership=%d sidecar ownership=%d", raw-rawBefore, nativeOwned, edgeOwned)
	}
	if err := f.store.WithinTx(context.Background(), func(tx *sqlite.Store) error {
		_, err := tx.UpdateEntityField(context.Background(), "members", "last_name", "live-0002", "Corrected")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, client := range []*testFinishStream{a, b} {
		_, data := client.next(t)
		if data.Rows[0].LastName != "Corrected" {
			t.Fatal(data)
		}
	}
	generation = f.store.FinishChanges().Generation()
	if _, err := service.NewRecounter(f.store, log.New(io.Discard, "", 0), false).Recount(context.Background(), "100", ""); err != nil {
		t.Fatal(err)
	}
	if f.store.FinishChanges().Generation() != generation+1 {
		t.Fatal("recount did not emit exactly one outer-commit hint")
	}
	if f.manager.Status("100").Errors != 0 {
		t.Fatal("ingest errors")
	}
}

func TestFinishStreamFailedProjectionNeverAnnouncesRawCommit(t *testing.T) {
	for _, sidecar := range []bool{false, true} {
		t.Run(fmt.Sprint(sidecar), func(t *testing.T) {
			f := newLiveFinishFixture(t, 1)
			before := f.store.FinishChanges().Generation()
			if _, err := f.store.DB().Exec(`CREATE TRIGGER synthetic_projection_failure BEFORE INSERT ON results BEGIN SELECT RAISE(ABORT,'synthetic projection failure'); END`); err != nil {
				t.Fatal(err)
			}
			var rawBefore int
			f.store.DB().QueryRow(`SELECT COUNT(*) FROM rfid_logs`).Scan(&rawBefore)
			payload, ack := f.frame(1, sidecar)
			err := f.exchange(payload, ack)
			if err == nil {
				t.Fatal("failed projection was ACKed")
			}
			if f.store.FinishChanges().Generation() != before {
				t.Fatal("failed projection emitted a finish hint")
			}
			var raw int
			if err := f.store.DB().QueryRow(`SELECT COUNT(*) FROM rfid_logs`).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			want := 1
			if sidecar {
				want = 0
			}
			if raw-rawBefore != want {
				t.Fatalf("raw delta=%d want %d", raw-rawBefore, want)
			}
			member, err := f.store.GetMember(context.Background(), "live-0001")
			if err != nil {
				t.Fatal(err)
			}
			if member.FinishTimeMs != nil || member.CleanTime != nil {
				t.Fatal("failed projection became a finish")
			}
		})
	}
}
