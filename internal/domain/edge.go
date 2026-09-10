package domain

// EdgeBinding authorizes an explicitly configured board/session for the event
// database. It is LAN provisioning, not a device authentication credential.
type EdgeBinding struct {
	Board           string `json:"board"`
	SourceSessionID string `json:"source_session_id"`
}

// EdgeRelayConfig selects the explicit Hub destination for this event only.
// It never changes captured observation event/session/source metadata.
type EdgeRelayConfig struct {
	Endpoint string `json:"endpoint"`
	Enabled  bool   `json:"enabled"`
	Revision int64  `json:"revision"`
}
