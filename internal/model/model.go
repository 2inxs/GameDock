package model

import "time"

// Server is persistent configuration, never an operating-system process handle.
type Server struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Game         string    `json:"game"`
	JavaPath     string    `json:"javaPath"`
	JarPath      string    `json:"jarPath"`
	JavaArgsFile string    `json:"javaArgsFile"`
	FilesDir     string    `json:"filesDir"`
	WorkingDir   string    `json:"workingDir"`
	MinMemoryMB  int       `json:"minMemoryMB"`
	MaxMemoryMB  int       `json:"maxMemoryMB"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

type Settings struct {
	LibraryDir string `json:"libraryDir"`
}

type ProcessState string

const (
	Stopped  ProcessState = "stopped"
	Starting ProcessState = "starting"
	Running  ProcessState = "running"
	Stopping ProcessState = "stopping"
	Failed   ProcessState = "failed"
)

type LaunchSpec struct {
	Executable  string
	Args        []string
	Env         []string
	WorkingDir  string
	StopCommand string
}

type RuntimeSnapshot struct {
	ServerID  string       `json:"serverId"`
	RunID     string       `json:"runId"`
	State     ProcessState `json:"state"`
	PID       int          `json:"pid"`
	StartedAt *time.Time   `json:"startedAt,omitempty"`
	ExitedAt  *time.Time   `json:"exitedAt,omitempty"`
	ExitCode  *int         `json:"exitCode,omitempty"`
	Error     string       `json:"error"`
}

type LogEntry struct {
	ServerID string    `json:"serverId"`
	RunID    string    `json:"runId"`
	Sequence uint64    `json:"sequence"`
	Time     time.Time `json:"time"`
	Stream   string    `json:"stream"`
	Message  string    `json:"message"`
}

type Event struct {
	Type     string           `json:"type"`
	ServerID string           `json:"serverId"`
	Snapshot *RuntimeSnapshot `json:"snapshot,omitempty"`
	Log      *LogEntry        `json:"log,omitempty"`
}

type ServerView struct {
	Config            Server          `json:"config"`
	Runtime           RuntimeSnapshot `json:"runtime"`
	Available         bool            `json:"available"`
	AvailabilityError string          `json:"availabilityError"`
}
