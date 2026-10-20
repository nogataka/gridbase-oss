package tunnelserver

import (
	"time"
)

// TunnelsAPIResponse represents the payload returned by /api/tunnels.
type TunnelsAPIResponse struct {
	GeneratedAt time.Time   `json:"generated_at"`
	Tunnels     []TunnelDTO `json:"tunnels"`
}

// TunnelDTO exposes tunnel metadata tailored for external APIs. The API key
// itself is never exposed; KeyID is its fingerprint (see APIKeyFingerprint).
type TunnelDTO struct {
	KeyID            string         `json:"key_id"`
	Mode             string         `json:"mode"`
	TunnelID         string         `json:"tunnel_id,omitempty"`
	Subdomains       []string       `json:"subdomains"`
	RemoteAddr       string         `json:"remote_addr"`
	ConnectedAt      time.Time      `json:"connected_at"`
	LastActivity     time.Time      `json:"last_activity"`
	PeerCommonName   string         `json:"peer_common_name,omitempty"`
	PeerSerialNumber string         `json:"peer_serial_number,omitempty"`
	BytesIn          int64          `json:"bytes_in"`
	BytesOut         int64          `json:"bytes_out"`
	ErrorCounts      map[string]int `json:"error_counts,omitempty"`
	LocalHost        string         `json:"local_host,omitempty"`
	LocalPort        int            `json:"local_port,omitempty"`
}

// MetricsAPIResponse summarizes counters exposed via metrics endpoints.
type MetricsAPIResponse struct {
	GeneratedAt       time.Time                   `json:"generated_at"`
	Active            int                         `json:"active_connections"`
	ActiveByMode      map[string]int              `json:"active_connections_by_mode"`
	BytesIn           map[string]int64            `json:"bytes_in"`
	BytesOut          map[string]int64            `json:"bytes_out"`
	BytesInByMode     map[string]map[string]int64 `json:"bytes_in_by_mode"`
	BytesOutByMode    map[string]map[string]int64 `json:"bytes_out_by_mode"`
	ErrorCounts       map[string]int              `json:"error_counts"`
	ErrorCountsByMode map[string]map[string]int   `json:"error_counts_by_mode"`
	CertExpiry        time.Time                   `json:"cert_expiry"`
	LastReload        time.Time                   `json:"last_reload"`
	Domains           map[string]Domain           `json:"domains"`
}

// Domain captures per-domain metrics.
type Domain struct {
	Mode        string         `json:"mode"`
	TunnelID    string         `json:"tunnel_id,omitempty"`
	Subdomains  []string       `json:"subdomains"`
	BytesIn     int64          `json:"bytes_in"`
	BytesOut    int64          `json:"bytes_out"`
	ErrorCounts map[string]int `json:"error_counts,omitempty"`
}

// BuildTunnelsResponse converts the live snapshot into an API response.
func (ts *TunnelServer) BuildTunnelsResponse() TunnelsAPIResponse {
	snapshot := ts.TunnelsSnapshot()
	dto := make([]TunnelDTO, len(snapshot))
	for i, tunnel := range snapshot {
		dto[i] = TunnelDTO{
			KeyID:            APIKeyFingerprint(tunnel.APIKey),
			Mode:             tunnel.Mode,
			TunnelID:         tunnel.TunnelID,
			Subdomains:       append([]string(nil), tunnel.Subdomains...),
			RemoteAddr:       tunnel.RemoteAddr,
			ConnectedAt:      tunnel.ConnectedAt,
			LastActivity:     tunnel.LastActivity,
			PeerCommonName:   tunnel.PeerCommonName,
			PeerSerialNumber: tunnel.PeerSerialNumber,
			BytesIn:          tunnel.BytesIn,
			BytesOut:         tunnel.BytesOut,
			LocalHost:        tunnel.LocalHost,
			LocalPort:        tunnel.LocalPort,
		}
		if len(tunnel.Errors) > 0 {
			dto[i].ErrorCounts = copyStringIntMap(tunnel.Errors)
		}
	}
	return TunnelsAPIResponse{
		GeneratedAt: time.Now(),
		Tunnels:     dto,
	}
}

// BuildMetricsResponse converts ServerStats into a shape suited for metrics publishing.
func (ts *TunnelServer) BuildMetricsResponse() MetricsAPIResponse {
	stats := ts.Stats()
	domainMetrics := make(map[string]Domain, len(stats.Domains))
	for _, domain := range stats.Domains {
		var errors map[string]int
		if len(domain.Errors) > 0 {
			errors = copyStringIntMap(domain.Errors)
		}
		// The connection key embeds the API key; publish a fingerprint-based key instead.
		domainKey := buildConnectionKey(APIKeyFingerprint(domain.APIKey), domain.Mode, domain.TunnelID)
		domainMetrics[domainKey] = Domain{
			Mode:        domain.Mode,
			TunnelID:    domain.TunnelID,
			Subdomains:  append([]string(nil), domain.Subdomains...),
			BytesIn:     domain.BytesIn,
			BytesOut:    domain.BytesOut,
			ErrorCounts: errors,
		}
	}
	return MetricsAPIResponse{
		GeneratedAt:       time.Now(),
		Active:            stats.ActiveConnections,
		ActiveByMode:      copyStringIntMap(stats.ActiveByMode),
		BytesIn:           fingerprintKeys(stats.BytesIn),
		BytesOut:          fingerprintKeys(stats.BytesOut),
		BytesInByMode:     fingerprintNestedKeys(stats.BytesInByMode),
		BytesOutByMode:    fingerprintNestedKeys(stats.BytesOutByMode),
		ErrorCounts:       copyStringIntMap(stats.ErrorCounts),
		ErrorCountsByMode: copyNestedIntMap(stats.ErrorCountsByMode),
		CertExpiry:        stats.CertExpiry,
		LastReload:        stats.LastReload,
		Domains:           domainMetrics,
	}
}

// fingerprintKeys re-keys a per-API-key counter map by API key fingerprint.
func fingerprintKeys(src map[string]int64) map[string]int64 {
	dst := make(map[string]int64, len(src))
	for apiKey, v := range src {
		dst[APIKeyFingerprint(apiKey)] += v
	}
	return dst
}

func fingerprintNestedKeys(src map[string]map[string]int64) map[string]map[string]int64 {
	dst := make(map[string]map[string]int64, len(src))
	for mode, values := range src {
		if len(values) == 0 {
			continue
		}
		dst[mode] = fingerprintKeys(values)
	}
	return dst
}
