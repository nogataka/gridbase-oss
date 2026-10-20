package tunnelserver

import "time"

// DomainRuntimeStats captures per-API-key runtime metrics for active tunnels.
type DomainRuntimeStats struct {
	APIKey           string
	Mode             string
	TunnelID         string
	Subdomains       []string
	RemoteAddr       string
	ConnectedAt      time.Time
	LastActivity     time.Time
	BytesIn          int64
	BytesOut         int64
	Errors           map[string]int
	PeerCommonName   string
	PeerSerialNumber string
}

// ServerStats aggregates server-wide runtime information for observability.
type ServerStats struct {
	ActiveConnections int
	ActiveByMode      map[string]int
	Domains           map[string]DomainRuntimeStats
	LastReload        time.Time
	CertExpiry        time.Time
	ErrorCounts       map[string]int
	ErrorCountsByMode map[string]map[string]int
	BytesIn           map[string]int64
	BytesOut          map[string]int64
	BytesInByMode     map[string]map[string]int64
	BytesOutByMode    map[string]map[string]int64
}

// TunnelInfo summarizes metadata about active tunnel connections for APIs.
// APIKey is internal and never serialised; API payloads use APIKeyFingerprint.
type TunnelInfo struct {
	APIKey           string         `json:"-"`
	Mode             string         `json:"mode,omitempty"`
	TunnelID         string         `json:"tunnel_id,omitempty"`
	Subdomains       []string       `json:"subdomains"`
	RemoteAddr       string         `json:"remote_addr"`
	ConnectedAt      time.Time      `json:"connected_at"`
	LastActivity     time.Time      `json:"last_activity"`
	PeerCommonName   string         `json:"peer_common_name"`
	PeerSerialNumber string         `json:"peer_serial_number"`
	BytesIn          int64          `json:"bytes_in"`
	BytesOut         int64          `json:"bytes_out"`
	Errors           map[string]int `json:"error_counts,omitempty"`
	LocalHost        string         `json:"local_host,omitempty"`
	LocalPort        int            `json:"local_port,omitempty"`
}
