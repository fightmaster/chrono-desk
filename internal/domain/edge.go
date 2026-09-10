package domain

// EdgeBinding authorizes an explicitly configured board/session for the event
// database. It is LAN provisioning, not a device authentication credential.
type EdgeBinding struct {
	Board           string `json:"board"`
	SourceSessionID string `json:"source_session_id"`
}
