package admin

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nogataka/gridbase-oss/tunnel/pkg/config"
	"github.com/nogataka/gridbase-oss/tunnel/pkg/tunnelserver"
)

// MetricsExporter renders Prometheus-compatible metrics derived from tunnel server state.
type MetricsExporter struct {
	now func() time.Time
}

// NewMetricsExporter constructs a metrics exporter.
func NewMetricsExporter() *MetricsExporter {
	return &MetricsExporter{
		now: time.Now,
	}
}

// WithClock injects a deterministic clock for testing.
func (m *MetricsExporter) WithClock(clock func() time.Time) *MetricsExporter {
	if clock != nil {
		m.now = clock
	}
	return m
}

// Render converts server statistics into Prometheus exposition format.
func (m *MetricsExporter) Render(stats tunnelserver.ServerStats, state config.RuntimeState) ([]byte, error) {
	now := m.now().UTC()
	var b strings.Builder

	writeGauge(&b,
		"gridbase_tunnel_active_connections",
		"Number of active tunnel connections.",
		float64(stats.ActiveConnections),
		nil,
	)

	writeGauge(&b,
		"gridbase_tunnel_active_domains",
		"Number of API keys with active tunnel connections.",
		float64(len(stats.Domains)),
		nil,
	)

	if !stats.LastReload.IsZero() {
		writeGauge(&b,
			"gridbase_config_last_reload_timestamp_seconds",
			"Unix timestamp of the last successful configuration reload.",
			float64(stats.LastReload.Unix()),
			nil,
		)
	}

	if !stats.CertExpiry.IsZero() {
		writeGauge(&b,
			"gridbase_tls_cert_expiry_timestamp_seconds",
			"Unix timestamp when the active TLS certificate expires.",
			float64(stats.CertExpiry.Unix()),
			nil,
		)
	}

	degraded := 0.0
	reason := state.Reason
	if reason == "" {
		reason = "none"
	}
	if state.Degraded {
		degraded = 1.0
	}
	writeGauge(&b,
		"gridbase_config_degraded",
		"Indicates whether configuration or downstream dependencies are degraded (1=degraded).",
		degraded,
		map[string]string{"reason": reason},
	)

	writeMetricHeader(&b,
		"gridbase_tunnel_bytes_total",
		"Total bytes processed per API key (by fingerprint) and direction.",
		"counter",
	)
	for _, apiKey := range sortedKeysInt64(stats.BytesIn) {
		value := float64(stats.BytesIn[apiKey])
		writeSample(&b, "gridbase_tunnel_bytes_total", map[string]string{
			"key_id":    tunnelserver.APIKeyFingerprint(apiKey),
			"direction": "in",
		}, value)
	}
	for _, apiKey := range sortedKeysInt64(stats.BytesOut) {
		value := float64(stats.BytesOut[apiKey])
		writeSample(&b, "gridbase_tunnel_bytes_total", map[string]string{
			"key_id":    tunnelserver.APIKeyFingerprint(apiKey),
			"direction": "out",
		}, value)
	}

	if len(stats.ErrorCounts) > 0 {
		writeMetricHeader(&b,
			"gridbase_tunnel_errors_total",
			"Aggregated error counts emitted by the tunnel server.",
			"counter",
		)
		for _, code := range sortedKeysInt(stats.ErrorCounts) {
			writeSample(&b, "gridbase_tunnel_errors_total", map[string]string{
				"code": code,
			}, float64(stats.ErrorCounts[code]))
		}
	}

	domainKeys := make([]string, 0, len(stats.Domains))
	for apiKey := range stats.Domains {
		domainKeys = append(domainKeys, apiKey)
	}
	sort.Strings(domainKeys)

	if len(domainKeys) > 0 {
		writeMetricHeader(&b,
			"gridbase_tunnel_connection_uptime_seconds",
			"Duration that each tunnel connection has been active.",
			"gauge",
		)
		writeMetricHeader(&b,
			"gridbase_tunnel_connection_last_activity_timestamp_seconds",
			"Last observed activity timestamp for each tunnel connection.",
			"gauge",
		)
		writeMetricHeader(&b,
			"gridbase_tunnel_connection_bytes_total",
			"Bytes transferred per active tunnel connection and direction.",
			"counter",
		)
		writeMetricHeader(&b,
			"gridbase_tunnel_connection_errors_total",
			"Error counts per active tunnel connection.",
			"counter",
		)
	}

	for _, apiKey := range domainKeys {
		domain := stats.Domains[apiKey]

		labels := map[string]string{
			"key_id":           tunnelserver.APIKeyFingerprint(domain.APIKey),
			"remote_addr":      domain.RemoteAddr,
			"peer_common_name": domain.PeerCommonName,
			"peer_serial":      domain.PeerSerialNumber,
			"subdomains":       strings.Join(domain.Subdomains, ";"),
		}

		uptime := now.Sub(domain.ConnectedAt).Seconds()
		if uptime < 0 {
			uptime = 0
		}
		writeSample(&b, "gridbase_tunnel_connection_uptime_seconds", labels, uptime)
		writeSample(&b, "gridbase_tunnel_connection_last_activity_timestamp_seconds", labels, float64(domain.LastActivity.Unix()))

		writeSample(&b, "gridbase_tunnel_connection_bytes_total", mergeLabels(labels, map[string]string{"direction": "in"}), float64(domain.BytesIn))
		writeSample(&b, "gridbase_tunnel_connection_bytes_total", mergeLabels(labels, map[string]string{"direction": "out"}), float64(domain.BytesOut))

		if len(domain.Errors) > 0 {
			for _, code := range sortedKeysInt(domain.Errors) {
				writeSample(&b, "gridbase_tunnel_connection_errors_total", mergeLabels(labels, map[string]string{"code": code}), float64(domain.Errors[code]))
			}
		} else {
			// Emit an explicit zero to aid alerting rules.
			writeSample(&b, "gridbase_tunnel_connection_errors_total", mergeLabels(labels, map[string]string{"code": "none"}), 0)
		}
	}

	return []byte(b.String()), nil
}

func writeGauge(b *strings.Builder, name, help string, value float64, labels map[string]string) {
	writeMetricHeader(b, name, help, "gauge")
	writeSample(b, name, labels, value)
}

func writeMetricHeader(b *strings.Builder, name, help, metricType string) {
	fmt.Fprintf(b, "# HELP %s %s\n", name, help)
	fmt.Fprintf(b, "# TYPE %s %s\n", name, metricType)
}

func writeSample(b *strings.Builder, name string, labels map[string]string, value float64) {
	fmt.Fprintf(b, "%s%s %s\n", name, formatLabels(labels), formatFloat(value))
}

func formatLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf(`%s="%s"`, k, escapeLabelValue(labels[k])))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func escapeLabelValue(value string) string {
	value = strings.ReplaceAll(value, "\\", `\\`)
	value = strings.ReplaceAll(value, "\n", `\n`)
	value = strings.ReplaceAll(value, "\"", `\"`)
	return value
}

func formatFloat(value float64) string {
	if value == 0 {
		return "0"
	}
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func mergeLabels(base map[string]string, extra map[string]string) map[string]string {
	if len(extra) == 0 {
		return base
	}
	merged := make(map[string]string, len(base)+len(extra))
	for k, v := range base {
		merged[k] = v
	}
	for k, v := range extra {
		merged[k] = v
	}
	return merged
}

func sortedKeysInt(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedKeysInt64(m map[string]int64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
