package main

import "time"

type User struct {
	ID                 string    `gorm:"primaryKey;size:64" json:"id"`
	Username           string    `gorm:"uniqueIndex;size:40" json:"username"`
	DisplayName        string    `json:"display_name"`
	PasswordHash       string    `json:"-"`
	Role               string    `json:"role"`
	Enabled            bool      `json:"enabled"`
	MustChangePassword bool      `json:"must_change_password"`
	CreatedAt          time.Time `json:"created_at"`
}
type LoginSession struct {
	TokenHash string `gorm:"primaryKey;size:64"`
	UserID    string `gorm:"index;size:64"`
	CSRF      string
	ExpiresAt time.Time `gorm:"index"`
}
type Device struct {
	ID                string     `gorm:"primaryKey;size:64" json:"id"`
	InstallationID    string     `gorm:"uniqueIndex;size:200" json:"-"`
	RegistrationHash  string     `json:"-"`
	TokenHash         string     `gorm:"uniqueIndex;size:64" json:"-"`
	Name              string     `json:"name"`
	Note              string     `json:"note"`
	Platform          string     `json:"platform"`
	OSVersion         string     `json:"os_version"`
	ClientVersion     string     `json:"client_version"`
	Capabilities      string     `json:"-"`
	Enabled           bool       `json:"enabled"`
	AgentID           string     `gorm:"index;size:64" json:"agent_id"`
	AssignmentID      string     `gorm:"size:64" json:"assignment_id"`
	AssignmentVersion uint64     `json:"assignment_version"`
	LastSeenAt        *time.Time `json:"last_seen_at"`
	LastImageAt       *time.Time `json:"last_image_at"`
	LastLocationAt    *time.Time `json:"last_location_at"`
	AppState          string     `json:"app_state"`
	CaptureState      string     `json:"capture_state"`
	ActiveSessionID   string     `json:"active_session_id"`
	LatestImageID     string     `json:"latest_image_id"`
	CreatedAt         time.Time  `json:"created_at"`
}
type Assignment struct {
	ID        string     `gorm:"primaryKey;size:64" json:"id"`
	DeviceID  string     `gorm:"index;size:64" json:"device_id"`
	AgentID   string     `gorm:"index;size:64" json:"agent_id"`
	Version   uint64     `json:"version"`
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at"`
}
type Command struct {
	ID                string     `gorm:"primaryKey;size:64" json:"id"`
	DeviceID          string     `gorm:"index;size:64" json:"device_id"`
	AssignmentID      string     `gorm:"size:64" json:"assignment_id"`
	AssignmentVersion uint64     `json:"assignment_version"`
	ActorID           string     `gorm:"index;size:64" json:"actor_id"`
	Type              string     `json:"type"`
	Parameters        string     `json:"-"`
	Status            string     `gorm:"index" json:"status"`
	SessionID         string     `gorm:"index;size:64" json:"session_id"`
	Result            string     `json:"-"`
	ErrorMessage      string     `json:"error_message"`
	ExpiresAt         time.Time  `gorm:"index" json:"expires_at"`
	CreatedAt         time.Time  `json:"created_at"`
	AckedAt           *time.Time `json:"acked_at"`
	CompletedAt       *time.Time `json:"completed_at"`
	LastAttemptAt     *time.Time `json:"-"`
}
type ImageSession struct {
	ID                string     `gorm:"primaryKey;size:64" json:"id"`
	DeviceID          string     `gorm:"index;size:64" json:"device_id"`
	AssignmentID      string     `gorm:"size:64" json:"assignment_id"`
	AssignmentVersion uint64     `json:"assignment_version"`
	ActorID           string     `json:"actor_id"`
	Version           uint64     `json:"version"`
	DesiredRunning    bool       `json:"desired_running"`
	ObservedState     string     `json:"observed_state"`
	StartCommandID    string     `json:"start_command_id"`
	StopCommandID     string     `json:"stop_command_id"`
	LastFrameSeq      uint64     `json:"last_frame_seq"`
	LastHistoryAt     *time.Time `json:"-"`
	TaskExpiresAt     *time.Time `json:"task_expires_at"`
	CreatedAt         time.Time  `json:"created_at"`
	StoppedAt         *time.Time `json:"stopped_at"`
}
type ImageReport struct {
	ID           string    `gorm:"primaryKey;size:64" json:"id"`
	DeviceID     string    `gorm:"uniqueIndex:device_report;index;size:64" json:"device_id"`
	ReportID     string    `gorm:"uniqueIndex:device_report;size:200" json:"report_id"`
	AssignmentID string    `gorm:"index;size:64" json:"assignment_id"`
	CommandID    string    `json:"command_id"`
	SessionID    string    `gorm:"index;size:64" json:"session_id"`
	FrameSeq     uint64    `json:"frame_seq"`
	FileName     string    `json:"-"`
	KeepHistory  bool      `json:"-"`
	Width        int       `json:"width"`
	Height       int       `json:"height"`
	Bytes        int64     `json:"bytes"`
	CapturedAt   time.Time `json:"captured_at"`
	ReceivedAt   time.Time `gorm:"index" json:"received_at"`
	IsCached     bool      `json:"is_cached"`
}
type LocationReport struct {
	ID               string    `gorm:"primaryKey;size:64" json:"id"`
	DeviceID         string    `gorm:"uniqueIndex:device_location;index;size:64" json:"device_id"`
	ReportID         string    `gorm:"uniqueIndex:device_location;size:200" json:"report_id"`
	AssignmentID     string    `gorm:"index;size:64" json:"assignment_id"`
	CommandID        string    `json:"command_id"`
	Latitude         float64   `json:"latitude"`
	Longitude        float64   `json:"longitude"`
	CoordinateSystem string    `json:"coordinate_system"`
	Accuracy         float64   `json:"horizontal_accuracy_m"`
	CollectedAt      time.Time `json:"collected_at"`
	ReceivedAt       time.Time `gorm:"index" json:"received_at"`
}
type AuditLog struct {
	ID        string    `gorm:"primaryKey;size:64" json:"id"`
	ActorID   string    `gorm:"index;size:64" json:"actor_id"`
	Action    string    `json:"action"`
	TargetID  string    `json:"target_id"`
	CreatedAt time.Time `gorm:"index" json:"created_at"`
}
