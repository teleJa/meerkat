package hub

type DeviceRow struct {
	ID              string
	Name            string
	Hostname        string
	OS              string
	Arch            string
	Role            string
	BootID          string
	Capabilities    string
	EdgeVersion     string
	ProtocolVersion string
	SecretHash      string
	CreatedAt       string
	LastSeenAt      *string
	LastMetricsAt   *string
	RevokedAt       *string
}

type MetricRow struct {
	ID               int64
	DeviceID         string
	SampledAt        string
	ReceivedAt       string
	CPUPct           *float64
	MemUsedBytes     *int64
	MemTotalBytes    *int64
	DiskUsedBytes    *int64
	DiskTotalBytes   *int64
	GPUUtilPct       *float64
	GPUMemUsedBytes  *int64
	GPUMemTotalBytes *int64
	GPUTempC         *float64
}

type MetricsPayload struct {
	CPUPct           *float64 `json:"cpu_pct"`
	MemUsedBytes     *int64   `json:"mem_used_bytes"`
	MemTotalBytes    *int64   `json:"mem_total_bytes"`
	DiskUsedBytes    *int64   `json:"disk_used_bytes"`
	DiskTotalBytes   *int64   `json:"disk_total_bytes"`
	GPUUtilPct       *float64 `json:"gpu_util_pct,omitempty"`
	GPUMemUsedBytes  *int64   `json:"gpu_mem_used_bytes,omitempty"`
	GPUMemTotalBytes *int64   `json:"gpu_mem_total_bytes,omitempty"`
	GPUTempC         *float64 `json:"gpu_temp_c,omitempty"`
}

type TaskRow struct {
	ID             string
	Title          string
	Source         string
	DeviceID       string
	SessionID      *string
	Status         string
	Stage          *string
	Cwd            string
	Request        string
	IdempotencyKey *string
	EvidenceKind   *string
	EvidenceNote   *string
	CreatedAt      string
	UpdatedAt      string
}

type SessionRow struct {
	ID               string
	TaskID           string
	DeviceID         string
	Mode             string
	Status           string
	Cwd              string
	Harness          *string
	HarnessVersion   *string
	HarnessSessionID *string
	CreatedAt        string
	LastActivityAt   string
}

type TurnRow struct {
	ID        string
	TaskID    string
	SessionID string
	Seq       int64
	Request   string
	Status    string
	StartedAt *string
	EndedAt   *string
	ExitCode  *int64
	Result    *string
	Error     *string
	CreatedAt string
}

type CommandRow struct {
	ID          string
	TaskID      string
	SessionID   *string
	TurnID      *string
	DeviceID    string
	Kind        string
	Status      string
	Payload     string
	Error       *string
	CreatedAt   string
	DeliveredAt *string
	AckedAt     *string
	ExpiresAt   string
}

type TaskEventRow struct {
	EventID    string
	TaskID     *string
	SessionID  *string
	TurnID     *string
	DeviceID   string
	Type       string
	Payload    string
	At         string
	ReceivedAt string
}

type EventRow struct {
	ID       int64   `json:"id"`
	At       string  `json:"at"`
	DeviceID *string `json:"device_id"`
	Type     string  `json:"type"`
	Detail   *string `json:"detail"`
}
type DeviceInfo struct {
	Name         string   `json:"name"`
	Hostname     string   `json:"hostname"`
	OS           string   `json:"os"`
	Arch         string   `json:"arch"`
	Role         string   `json:"role"`
	Capabilities []string `json:"capabilities"`
	BootID       string   `json:"boot_id"`
}

type CreateTaskInput struct {
	Request        string
	Title          string
	DeviceID       string
	Cwd            string
	RunProfile     *string
	IdempotencyKey *string
	TimeoutSeconds *int
	AttachmentIDs  []string
	AttachmentRows []AttachmentRow
}

type TurnCompletedInput struct {
	TaskID    string
	SessionID string
	TurnID    string
	Status    string
	ExitCode  *int64
	Result    *string
	Error     *string
	At        string
}
type ArtifactRow struct {
	ID        string
	TaskID    string
	DeviceID  string
	TurnID    *string
	Name      string
	Path      string
	Mime      string
	Size      int64
	SHA256    string
	CreatedAt string
}
type AttachmentRow struct {
	ID        string
	TaskID    *string
	TurnID    *string
	DeviceID  *string
	Name      string
	Mime      string
	Size      int64
	SHA256    string
	CreatedAt string
}
