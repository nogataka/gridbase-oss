package tunnelserver

import "testing"

func TestExtractSubdomain(t *testing.T) {
	tests := []struct {
		name string
		host string
		base string
		want string
	}{
		{"simple", "foo.gridbase.localhost", "gridbase.localhost", "foo"},
		{"with port", "foo.gridbase.localhost:8080", "gridbase.localhost", "foo"},
		{"uppercase host", "FOO.GridBase.Localhost", "gridbase.localhost", "foo"},
		{"uppercase base", "foo.gridbase.localhost", "GridBase.Localhost", "foo"},
		{"trailing dot", "foo.gridbase.localhost.", "gridbase.localhost", "foo"},
		{"two-label base", "app.example.com", "example.com", "app"},
		{"deep base", "app.tunnel.example.com", "tunnel.example.com", "app"},
		{"nested subdomain keeps prefix", "a.b.example.com", "example.com", "a.b"},
		{"apex is not a subdomain", "gridbase.localhost", "gridbase.localhost", ""},
		{"apex with port", "gridbase.localhost:80", "gridbase.localhost", ""},
		{"foreign host", "foo.other.org", "example.com", ""},
		{"suffix without dot boundary", "fooexample.com", "example.com", ""},
		{"base only as prefix", "example.com.evil.org", "example.com", ""},
		{"empty host", "", "example.com", ""},
		{"empty base", "foo.example.com", "", ""},
		{"wildcard base normalised", "foo.example.com", "*.example.com", "foo"},
		{"ipv6 literal", "[::1]:8080", "example.com", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExtractSubdomain(tt.host, tt.base); got != tt.want {
				t.Fatalf("ExtractSubdomain(%q, %q) = %q, want %q", tt.host, tt.base, got, tt.want)
			}
		})
	}
}
