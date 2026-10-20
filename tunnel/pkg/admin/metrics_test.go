package admin

import (
	"strings"
	"testing"
	"time"

	"github.com/nogataka/gridbase-oss/tunnel/pkg/config"
	"github.com/nogataka/gridbase-oss/tunnel/pkg/tunnelserver"
)

func TestMetricsExporterRender(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	exporter := NewMetricsExporter().WithClock(func() time.Time { return now })

	stats := tunnelserver.ServerStats{
		ActiveConnections: 1,
		BytesIn:           map[string]int64{"alpha": 128},
		BytesOut:          map[string]int64{"alpha": 256},
		ErrorCounts:       map[string]int{"forward_send": 2},
		CertExpiry:        now.Add(24 * time.Hour),
		LastReload:        now.Add(-time.Minute),
		Domains: map[string]tunnelserver.DomainRuntimeStats{
			"alpha": {
				APIKey:           "alpha",
				RemoteAddr:       "198.51.100.10:443",
				ConnectedAt:      now.Add(-5 * time.Minute),
				LastActivity:     now.Add(-30 * time.Second),
				BytesIn:          128,
				BytesOut:         256,
				Subdomains:       []string{"app"},
				Errors:           map[string]int{"forward_send": 1},
				PeerCommonName:   "client.gridbase.dev",
				PeerSerialNumber: "abcd",
			},
		},
	}

	state := config.RuntimeState{Degraded: true, Reason: "directus watch"}

	payload, err := exporter.Render(stats, state)
	if err != nil {
		t.Fatalf("Render error: %v", err)
	}

	body := string(payload)
	keyID := tunnelserver.APIKeyFingerprint("alpha")
	if strings.Contains(body, "api_key") || strings.Contains(body, `"alpha"`) {
		t.Fatalf("metrics must label series by key fingerprint, not by API key:\n%s", body)
	}
	assertContains(t, body, "gridbase_tunnel_active_connections 1")
	assertContains(t, body, "gridbase_config_degraded{reason=\"directus watch\"} 1")
	assertContains(t, body, "gridbase_tunnel_bytes_total{direction=\"in\",key_id=\""+keyID+"\"} 128")
	assertContains(t, body, "gridbase_tunnel_connection_uptime_seconds{key_id=\""+keyID+"\"")
	assertContains(t, body, "gridbase_tunnel_connection_errors_total{code=\"forward_send\",key_id=\""+keyID+"\"")
}

func assertContains(t *testing.T, body, needle string) {
	t.Helper()
	if !strings.Contains(body, needle) {
		t.Fatalf("expected metrics payload to contain %q. Payload:\n%s", needle, body)
	}
}
