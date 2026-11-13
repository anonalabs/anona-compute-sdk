// Package api is a small client for the E2B-compatible REST API served by the
// Anona gateway.
//
// Types and routes follow the E2B OpenAPI spec at
// e2b-dev/E2B@ee355b765ac0e884fbeaa69e4b44f4c7c58ed6b1 (release 2.61.0),
// file spec/openapi.yml. A copy of that file is kept in
// cli/internal/fakee2b/testdata/openapi.yml and the tests check every route
// used here against it.
package api

import (
	"encoding/json"
	"fmt"
	"time"
)

// Sandbox is components.schemas.Sandbox (create, connect, resume and fork
// responses).
type Sandbox struct {
	TemplateID         string  `json:"templateID"`
	SandboxID          string  `json:"sandboxID"`
	Alias              string  `json:"alias,omitempty"`
	ClientID           string  `json:"clientID"`
	EnvdVersion        string  `json:"envdVersion"`
	EnvdAccessToken    string  `json:"envdAccessToken,omitempty"`
	TrafficAccessToken *string `json:"trafficAccessToken,omitempty"`
	Domain             *string `json:"domain,omitempty"`
}

// SandboxDetail is components.schemas.SandboxDetail (GET /sandboxes/{id}).
type SandboxDetail struct {
	TemplateID          string    `json:"templateID"`
	Alias               string    `json:"alias,omitempty"`
	SandboxID           string    `json:"sandboxID"`
	ClientID            string    `json:"clientID"`
	StartedAt           time.Time `json:"startedAt"`
	EndAt               time.Time `json:"endAt"`
	EnvdVersion         string    `json:"envdVersion"`
	EnvdAccessToken     string    `json:"envdAccessToken,omitempty"`
	AllowInternetAccess *bool     `json:"allowInternetAccess,omitempty"`
	Domain              *string   `json:"domain,omitempty"`
	CPUCount            int32     `json:"cpuCount"`
	MemoryMB            int32     `json:"memoryMB"`
	DiskSizeMB          int32     `json:"diskSizeMB"`
	// Size is the size the sandbox was created at (small, medium, large,
	// xl); empty when it runs at its template's own size.
	Size     string            `json:"size,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
	State    string            `json:"state"`
}

// ListedSandbox is components.schemas.ListedSandbox (GET /v2/sandboxes).
type ListedSandbox struct {
	TemplateID  string            `json:"templateID"`
	Alias       string            `json:"alias,omitempty"`
	SandboxID   string            `json:"sandboxID"`
	ClientID    string            `json:"clientID"`
	StartedAt   time.Time         `json:"startedAt"`
	EndAt       time.Time         `json:"endAt"`
	CPUCount    int32             `json:"cpuCount"`
	MemoryMB    int32             `json:"memoryMB"`
	DiskSizeMB  int32             `json:"diskSizeMB"`
	Size        string            `json:"size,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	State       string            `json:"state"`
	EnvdVersion string            `json:"envdVersion"`
}

// NewSandboxV2 is components.schemas.NewSandboxV2 (POST /v2/sandboxes). CPU,
// memory and disk come from the template, or from Size when it is set.
type NewSandboxV2 struct {
	TemplateID          string            `json:"templateID"`
	Timeout             *int32            `json:"timeout,omitempty"`
	AutoPause           *AutoPause        `json:"autoPause,omitempty"`
	AllowInternetAccess *bool             `json:"allow_internet_access,omitempty"`
	Metadata            map[string]string `json:"metadata,omitempty"`
	EnvVars             map[string]string `json:"envVars,omitempty"`
	// Network is the egress policy for this sandbox, passed through as the
	// gateway received it (its egress spec). Raw rather than typed: the
	// spec carries more than the CLI has flags for, and the gateway is what
	// validates it.
	Network json.RawMessage `json:"network,omitempty"`
	// Size is an Anona extension: small, medium, large or xl. Empty runs the
	// sandbox at its template's own size.
	Size string `json:"size,omitempty"`
}

// AutoPause is the create body's "autoPause". The gateway accepts two shapes
// and they mean different things:
//
//   - a boolean: pause instead of delete when the sandbox's DEADLINE runs out.
//   - {"idleMinutes": n}: pause the sandbox once nothing has used it for n
//     minutes (the gateway's idle pauser), AND pause instead of delete at the
//     deadline, as the boolean true does. n is 1 to 10080 (one week), 0 turns
//     idle pausing off, and {} takes the team default, else 15.
//
// So the object form always carries pause-at-deadline; there is no way to ask
// for idle pausing alone, which is why create --idle-pause off without
// --auto-pause sends no autoPause at all.
type AutoPause struct {
	On          bool
	IdleMinutes *int
}

// MarshalJSON writes the object form when IdleMinutes is set, else the boolean.
func (a AutoPause) MarshalJSON() ([]byte, error) {
	if a.IdleMinutes != nil {
		return json.Marshal(struct {
			IdleMinutes int `json:"idleMinutes"`
		}{*a.IdleMinutes})
	}
	return json.Marshal(a.On)
}

// UnmarshalJSON accepts either shape, so a round trip through the API keeps it.
func (a *AutoPause) UnmarshalJSON(b []byte) error {
	var flag bool
	if err := json.Unmarshal(b, &flag); err == nil {
		a.On, a.IdleMinutes = flag, nil
		return nil
	}
	var obj struct {
		IdleMinutes *int `json:"idleMinutes"`
	}
	if err := json.Unmarshal(b, &obj); err != nil {
		return fmt.Errorf("autoPause must be a boolean or an object like {\"idleMinutes\": 15}")
	}
	a.On, a.IdleMinutes = true, obj.IdleMinutes
	return nil
}

// SandboxForkResult is components.schemas.SandboxForkResult. Exactly one of
// Sandbox and Error is set.
type SandboxForkResult struct {
	Sandbox *Sandbox `json:"sandbox,omitempty"`
	Error   *Error   `json:"error,omitempty"`
}

// SandboxLogEntry is components.schemas.SandboxLogEntry.
type SandboxLogEntry struct {
	Timestamp time.Time         `json:"timestamp"`
	Message   string            `json:"message"`
	Level     string            `json:"level"`
	Fields    map[string]string `json:"fields"`
}

// SandboxMetric is components.schemas.SandboxMetric: one sample of a
// sandbox's resource use, from the sandbox's own envd.
type SandboxMetric struct {
	Timestamp     time.Time `json:"timestamp"`
	TimestampUnix int64     `json:"timestampUnix"`
	CPUCount      int       `json:"cpuCount"`
	CPUUsedPct    float64   `json:"cpuUsedPct"`
	MemUsed       int64     `json:"memUsed"`
	MemTotal      int64     `json:"memTotal"`
	MemCache      int64     `json:"memCache"`
	DiskUsed      int64     `json:"diskUsed"`
	DiskTotal     int64     `json:"diskTotal"`
}

// SandboxLogsV2Response is components.schemas.SandboxLogsV2Response.
type SandboxLogsV2Response struct {
	Logs []SandboxLogEntry `json:"logs"`
}
