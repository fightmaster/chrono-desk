package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"

	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
	"gitlab.com/fightmaster1/chrono-desk/internal/service"
	"gitlab.com/fightmaster1/chrono-desk/internal/version"
)

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, version.Get())
}

func (s *Server) handleListEvents(w http.ResponseWriter, r *http.Request) {
	infos, err := s.events.List(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	if infos == nil {
		infos = []service.EventInfo{}
	}
	writeJSON(w, http.StatusOK, infos)
}

func (s *Server) handleImportEvent(w http.ResponseWriter, r *http.Request) {
	stats, err := s.events.ImportExport(r.Context(), http.MaxBytesReader(w, r.Body, 512<<20))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func (s *Server) handleRfidImport(w http.ResponseWriter, r *http.Request) {
	eventID := r.PathValue("id")
	device := r.URL.Query().Get("device")
	tz := r.URL.Query().Get("tz")
	if tz == "" {
		s.fail(w, fmt.Errorf("укажите таймзону (tz): CSV Feibot хранит локальное время без зоны"))
		return
	}

	store, err := s.events.Open(eventID)
	if err != nil {
		s.fail(w, err)
		return
	}

	res, err := service.NewFeibotCsvImporter(store).
		Import(r.Context(), http.MaxBytesReader(w, r.Body, 512<<20), eventID, device, tz)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleRecount(w http.ResponseWriter, r *http.Request) {
	eventID := r.PathValue("id")
	raceID := r.URL.Query().Get("race")
	store, err := s.events.Open(eventID)
	if err != nil {
		s.fail(w, err)
		return
	}
	stats, err := service.NewRecounter(store, s.logger, false).Recount(r.Context(), eventID, raceID)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func (s *Server) handleListRaces(w http.ResponseWriter, r *http.Request) {
	store, err := s.events.Open(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	races, err := store.ListRaces(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}

	type raceJSON struct {
		ID                          string `json:"id"`
		Name                        string `json:"name"`
		Date                        string `json:"date"`
		Format                      string `json:"format"`
		StartedAtMs                 *int64 `json:"started_at_ms"`
		CategoryExcludesTopByGender bool   `json:"category_excludes_top_by_gender"`
	}
	out := make([]raceJSON, 0, len(races))
	for _, rc := range races {
		out = append(out, raceJSON{
			ID: rc.ID, Name: rc.Name, Date: rc.Date, Format: string(rc.Format),
			StartedAtMs:                 rc.StartedAtMs,
			CategoryExcludesTopByGender: rc.CategoryExcludesTopByGender,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleProtocol(w http.ResponseWriter, r *http.Request) {
	store, err := s.events.Open(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	protocol, err := service.BuildProtocol(r.Context(), store, r.PathValue("raceID"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, protocol)
}

func (s *Server) handleProtocolXLSX(w http.ResponseWriter, r *http.Request) {
	store, err := s.events.Open(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	data, name, err := service.BuildProtocolXLSX(r.Context(), store, r.PathValue("raceID"))
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	_, _ = w.Write(data)
}

func (s *Server) handleExportXLSX(w http.ResponseWriter, r *http.Request) {
	store, err := s.events.Open(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	data, name, err := service.BuildProtocolXLSX(r.Context(), store, r.PathValue("raceID"))
	if err != nil {
		s.fail(w, err)
		return
	}
	path, err := service.SaveToDownloads(name, data)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": path})
}

func (s *Server) handleApplyEdit(w http.ResponseWriter, r *http.Request) {
	store, err := s.events.Open(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	var req service.EditRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		s.fail(w, err)
		return
	}
	res, err := service.ApplyEdit(r.Context(), store, req)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleListEdits(w http.ResponseWriter, r *http.Request) {
	store, err := s.events.Open(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	changes, err := store.ListLocalChanges(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	if changes == nil {
		changes = []sqlite.LocalChange{}
	}
	writeJSON(w, http.StatusOK, changes)
}

func (s *Server) handleListCheckpoints(w http.ResponseWriter, r *http.Request) {
	store, err := s.events.Open(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	checkpoints, err := store.ListCheckpointsByEvent(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, checkpoints)
}

func (s *Server) handleMemberPasses(w http.ResponseWriter, r *http.Request) {
	store, err := s.events.Open(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	passes, err := service.LoadMemberPasses(r.Context(), store, r.PathValue("memberID"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, passes)
}

func (s *Server) handleCreateMember(w http.ResponseWriter, r *http.Request) {
	store, err := s.events.Open(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	var req service.CreateMemberRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		s.fail(w, err)
		return
	}
	memberID, res, err := service.CreateMember(r.Context(), store, r.PathValue("id"), req)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"member_id": memberID, "recount_needed": res.RecountNeeded})
}

func (s *Server) handleGetMember(w http.ResponseWriter, r *http.Request) {
	store, err := s.events.Open(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	m, err := store.GetMember(r.Context(), r.PathValue("memberID"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": m.ID, "race_id": m.RaceID, "category_id": m.CategoryID,
		"number": m.Number, "epc": m.EPC,
		"first_name": m.FirstName, "last_name": m.LastName,
		"gender": m.Gender, "dob": m.DOB, "team": m.Team, "city": m.City,
	})
}

type categoryJSON struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (s *Server) handleListCategories(w http.ResponseWriter, r *http.Request) {
	store, err := s.events.Open(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	categories, err := store.ListCategories(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	out := make([]categoryJSON, 0, len(categories))
	for _, c := range categories {
		out = append(out, categoryJSON{ID: c.ID, Name: c.Name})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleListRaceCategories(w http.ResponseWriter, r *http.Request) {
	store, err := s.events.Open(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	categories, err := store.ListRaceCategories(r.Context(), r.PathValue("raceID"))
	if err != nil {
		s.fail(w, err)
		return
	}
	out := make([]categoryJSON, 0, len(categories))
	for _, c := range categories {
		out = append(out, categoryJSON{ID: c.ID, Name: c.Name})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleAttachCategory(w http.ResponseWriter, r *http.Request) {
	store, err := s.events.Open(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	var req struct {
		CategoryID string `json:"category_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		s.fail(w, err)
		return
	}
	res, err := service.AttachCategory(r.Context(), store, r.PathValue("id"), r.PathValue("raceID"), req.CategoryID)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleDetachCategory(w http.ResponseWriter, r *http.Request) {
	store, err := s.events.Open(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	res, err := service.DetachCategory(r.Context(), store, r.PathValue("id"), r.PathValue("raceID"), r.PathValue("categoryID"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleListMembers(w http.ResponseWriter, r *http.Request) {
	store, err := s.events.Open(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	members, err := store.ListMembersByEvent(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, members)
}

func (s *Server) handleCreateCheckpoint(w http.ResponseWriter, r *http.Request) {
	store, err := s.events.Open(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	var req service.CreateCheckpointRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		s.fail(w, err)
		return
	}
	id, res, err := service.CreateCheckpoint(r.Context(), store, r.PathValue("id"), req)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"checkpoint_id": id, "recount_needed": res.RecountNeeded})
}

func (s *Server) handleDeleteCheckpoint(w http.ResponseWriter, r *http.Request) {
	store, err := s.events.Open(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	res, err := service.DeleteCheckpoint(r.Context(), store, r.PathValue("id"), r.PathValue("cpID"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleBackup(w http.ResponseWriter, r *http.Request) {
	store, err := s.events.Open(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	dir, err := service.DownloadsDir()
	if err != nil {
		s.fail(w, err)
		return
	}
	path, err := service.SnapshotEvent(r.Context(), store, r.PathValue("id"), dir)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": path})
}

func (s *Server) handleExportJSON(w http.ResponseWriter, r *http.Request) {
	store, err := s.events.Open(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	data, name, err := service.BuildEventExport(r.Context(), store, r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	path, err := service.SaveToDownloads(name, data)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": path})
}
