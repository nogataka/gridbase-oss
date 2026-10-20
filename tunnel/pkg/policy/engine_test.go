package policy

import (
	"errors"
	"net"
	"net/http"
	"reflect"
	"testing"

	"github.com/nogataka/gridbase-oss/tunnel/pkg/config"
)

func TestEngineEvaluateRequest(t *testing.T) {
	testCases := []struct {
		name         string
		policy       config.DomainPolicy
		trusted      []string
		ctx          RequestContext
		wantHeaders  map[string]string
		wantErrCode  ViolationCode
		wantHTTPCode int
		wantDetails  map[string]string
		mutateResult bool
	}{
		{
			name:        "no restrictions returns nil headers",
			policy:      config.DomainPolicy{},
			ctx:         RequestContext{},
			wantHeaders: nil,
		},
		{
			name: "protocol violation when header mismatches allowed list",
			policy: config.DomainPolicy{
				AllowedProtocols: []string{"https"},
			},
			ctx: RequestContext{
				Header: http.Header{
					"X-Forwarded-Proto": []string{"http"},
				},
			},
			wantErrCode:  ViolationProtocol,
			wantHTTPCode: http.StatusForbidden,
			wantDetails: map[string]string{
				"protocol": "http",
			},
		},
		{
			name: "ip allow list accepts CIDR using remote address fallback",
			policy: config.DomainPolicy{
				IPAllowList: []string{"10.0.0.0/8"},
			},
			ctx: RequestContext{
				RemoteAddr: "10.12.0.5:44321",
			},
			wantHeaders: nil,
		},
		{
			name: "missing client ip is rejected",
			policy: config.DomainPolicy{
				IPAllowList: []string{"192.168.1.10"},
			},
			ctx:          RequestContext{},
			wantErrCode:  ViolationIP,
			wantHTTPCode: http.StatusForbidden,
		},
		{
			name: "invalid ip string from a trusted proxy is rejected with detail",
			policy: config.DomainPolicy{
				IPAllowList: []string{"192.168.1.10"},
			},
			trusted: []string{"10.0.0.0/8"},
			ctx: RequestContext{
				RemoteAddr: "10.0.0.2:5555",
				Header: http.Header{
					"X-Forwarded-For": []string{"not-an-ip"},
				},
			},
			wantErrCode:  ViolationIP,
			wantHTTPCode: http.StatusForbidden,
			wantDetails: map[string]string{
				"client_ip": "not-an-ip",
			},
		},
		{
			name: "forged X-Forwarded-For is ignored without trusted proxies",
			policy: config.DomainPolicy{
				IPAllowList: []string{"192.168.1.10"},
			},
			ctx: RequestContext{
				RemoteAddr: "203.0.113.50:4000",
				Header: http.Header{
					"X-Forwarded-For": []string{"192.168.1.10"},
					"X-Real-Ip":       []string{"192.168.1.10"},
				},
			},
			wantErrCode:  ViolationIP,
			wantHTTPCode: http.StatusForbidden,
			wantDetails:  map[string]string{"client_ip": "203.0.113.50"},
		},
		{
			name: "forged X-Forwarded-For from an untrusted peer is ignored even when proxies are configured",
			policy: config.DomainPolicy{
				IPAllowList: []string{"192.168.1.10"},
			},
			trusted: []string{"10.0.0.0/8"},
			ctx: RequestContext{
				RemoteAddr: "203.0.113.50:4000",
				Header: http.Header{
					"X-Forwarded-For": []string{"192.168.1.10"},
				},
			},
			wantErrCode:  ViolationIP,
			wantHTTPCode: http.StatusForbidden,
			wantDetails:  map[string]string{"client_ip": "203.0.113.50"},
		},
		{
			name: "trusted proxy: client is the first untrusted hop from the right",
			policy: config.DomainPolicy{
				IPAllowList: []string{"198.51.100.7"},
			},
			trusted: []string{"10.0.0.0/8"},
			ctx: RequestContext{
				RemoteAddr: "10.0.0.2:5555",
				Header: http.Header{
					"X-Forwarded-For": []string{"198.51.100.7, 10.0.0.9"},
				},
			},
		},
		{
			name: "trusted proxy: a value prepended by the visitor does not win",
			policy: config.DomainPolicy{
				IPAllowList: []string{"192.168.1.10"},
			},
			trusted: []string{"10.0.0.0/8"},
			ctx: RequestContext{
				RemoteAddr: "10.0.0.2:5555",
				Header: http.Header{
					"X-Forwarded-For": []string{"192.168.1.10, 203.0.113.50"},
				},
			},
			wantErrCode:  ViolationIP,
			wantHTTPCode: http.StatusForbidden,
			wantDetails:  map[string]string{"client_ip": "203.0.113.50"},
		},
		{
			name: "request size exceeding limit returns violation",
			policy: config.DomainPolicy{
				MaxRequestBytes: 64,
			},
			ctx: RequestContext{
				BodySize: 128,
			},
			wantErrCode:  ViolationRequestSize,
			wantHTTPCode: http.StatusRequestEntityTooLarge,
			wantDetails: map[string]string{
				"body_size":        "128",
				"max_request_size": "64",
			},
		},
		{
			name: "default headers are copied on success",
			policy: config.DomainPolicy{
				DefaultHeaders: map[string]string{
					"X-Trace-ID": "trace-123",
					"X-Env":      "prod",
				},
			},
			ctx: RequestContext{},
			wantHeaders: map[string]string{
				"X-Trace-ID": "trace-123",
				"X-Env":      "prod",
			},
			mutateResult: true,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			engine := NewEngine()
			engine.SetTrustedProxies(tc.trusted)
			headers, err := engine.EvaluateRequest(tc.policy, tc.ctx)

			if tc.wantErrCode == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if !reflect.DeepEqual(headers, tc.wantHeaders) {
					t.Fatalf("unexpected headers: got %#v want %#v", headers, tc.wantHeaders)
				}
				if tc.mutateResult && headers != nil {
					headers["X-Trace-ID"] = "mutated"
					if tc.policy.DefaultHeaders["X-Trace-ID"] != "trace-123" {
						t.Fatalf("policy default headers mutated unexpectedly: %#v", tc.policy.DefaultHeaders)
					}
				}
				return
			}

			var vErr *ViolationError
			if err == nil || !errors.As(err, &vErr) {
				t.Fatalf("expected ViolationError, got %v", err)
			}
			if vErr.Code != tc.wantErrCode {
				t.Fatalf("unexpected violation code: got %s want %s", vErr.Code, tc.wantErrCode)
			}
			if vErr.HTTPStatus != tc.wantHTTPCode {
				t.Fatalf("unexpected status: got %d want %d", vErr.HTTPStatus, tc.wantHTTPCode)
			}
			for key, value := range tc.wantDetails {
				if v, ok := vErr.Details[key]; !ok || v != value {
					t.Fatalf("expected detail %s=%s, got %#v", key, value, vErr.Details)
				}
			}
		})
	}
}

func TestEngineEvaluateTCP(t *testing.T) {
	testCases := []struct {
		name        string
		policy      config.DomainPolicy
		ctx         TCPContext
		wantErrCode ViolationCode
	}{
		{
			name:   "no restrictions allows connection",
			policy: config.DomainPolicy{},
			ctx:    TCPContext{RemoteAddr: "203.0.113.10:4000"},
		},
		{
			name: "ip allow list accepts matching remote",
			policy: config.DomainPolicy{
				IPAllowList: []string{"203.0.113.10"},
			},
			ctx: TCPContext{RemoteAddr: "203.0.113.10:4000"},
		},
		{
			name: "ip allow list rejects mismatched remote",
			policy: config.DomainPolicy{
				IPAllowList: []string{"203.0.113.10"},
			},
			ctx:         TCPContext{RemoteAddr: "198.51.100.25:1000"},
			wantErrCode: ViolationIP,
		},
		{
			name: "missing remote address fails when allow list present",
			policy: config.DomainPolicy{
				IPAllowList: []string{"10.0.0.1"},
			},
			ctx:         TCPContext{},
			wantErrCode: ViolationIP,
		},
	}

	for _, tc := range testCases {
		c := tc
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			engine := NewEngine()
			err := engine.EvaluateTCP(c.policy, c.ctx)
			if c.wantErrCode == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}

			var vErr *ViolationError
			if err == nil || !errors.As(err, &vErr) {
				t.Fatalf("expected violation error, got %v", err)
			}
			if vErr.Code != c.wantErrCode {
				t.Fatalf("unexpected violation code: got %s want %s", vErr.Code, c.wantErrCode)
			}
		})
	}
}

func TestDetectProtocol(t *testing.T) {
	trusted, err := config.ParseTrustedProxies([]string{"10.0.0.0/8"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	xfp := func(values ...string) http.Header {
		h := http.Header{}
		for _, v := range values {
			h.Add("X-Forwarded-Proto", v)
		}
		return h
	}

	cases := []struct {
		name    string
		trusted []*net.IPNet
		ctx     RequestContext
		want    string
	}{
		{"untrusted peer claiming https is http", trusted, RequestContext{RemoteAddr: "203.0.113.9:1", Header: xfp("https")}, "http"},
		{"no trusted proxies configured ignores the header", nil, RequestContext{RemoteAddr: "10.0.0.2:1", Header: xfp("https")}, "http"},
		{"untrusted peer over TLS is https", trusted, RequestContext{RemoteAddr: "203.0.113.9:1", TLS: true, Header: xfp("http")}, "https"},
		{"direct TLS without header is https", nil, RequestContext{RemoteAddr: "203.0.113.9:1", TLS: true}, "https"},
		{"trusted peer: header is adopted", trusted, RequestContext{RemoteAddr: "10.0.0.2:1", Header: xfp("https")}, "https"},
		{"trusted peer: header value is normalised", trusted, RequestContext{RemoteAddr: "10.0.0.2:1", Header: xfp(" HTTPS ")}, "https"},
		{"trusted peer: header may downgrade a TLS hop", trusted, RequestContext{RemoteAddr: "10.0.0.2:1", TLS: true, Header: xfp("http")}, "http"},
		{"trusted peer: no header falls back to the connection", trusted, RequestContext{RemoteAddr: "10.0.0.2:1", Header: http.Header{}}, "http"},
		{"trusted peer: rightmost value wins over a visitor-prepended one", trusted, RequestContext{RemoteAddr: "10.0.0.2:1", Header: xfp("https, http")}, "http"},
		{"trusted peer: rightmost across header lines", trusted, RequestContext{RemoteAddr: "10.0.0.2:1", Header: xfp("https", "http")}, "http"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := detectProtocol(tc.ctx, tc.trusted); got != tc.want {
				t.Fatalf("detectProtocol = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestEngineProtocolPolicyIgnoresForgedForwardedProto(t *testing.T) {
	httpsOnly := config.DomainPolicy{AllowedProtocols: []string{"https"}}
	forged := http.Header{"X-Forwarded-Proto": []string{"https"}}

	cases := []struct {
		name    string
		trusted []string
		ctx     RequestContext
		wantErr bool
	}{
		{"forged https from untrusted peer is rejected", []string{"10.0.0.0/8"}, RequestContext{RemoteAddr: "203.0.113.9:1", Header: forged}, true},
		{"forged https with no trusted proxies is rejected", nil, RequestContext{RemoteAddr: "203.0.113.9:1", Header: forged}, true},
		{"https via a trusted proxy is accepted", []string{"10.0.0.0/8"}, RequestContext{RemoteAddr: "10.0.0.2:1", Header: forged}, false},
		{"plain http via a trusted proxy is rejected", []string{"10.0.0.0/8"}, RequestContext{RemoteAddr: "10.0.0.2:1", Header: http.Header{"X-Forwarded-Proto": []string{"http"}}}, true},
		{"direct TLS is accepted", nil, RequestContext{RemoteAddr: "203.0.113.9:1", TLS: true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			engine := NewEngine()
			engine.SetTrustedProxies(tc.trusted)
			_, err := engine.EvaluateRequest(httpsOnly, tc.ctx)
			var vErr *ViolationError
			if tc.wantErr {
				if !errors.As(err, &vErr) || vErr.Code != ViolationProtocol {
					t.Fatalf("expected protocol violation, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestStripUntrustedForwarding(t *testing.T) {
	build := func() http.Header {
		h := http.Header{}
		h.Set("X-Forwarded-For", "1.2.3.4")
		h.Set("X-Forwarded-Proto", "https")
		h.Set("X-Forwarded-Host", "evil.example")
		h.Set("X-Real-IP", "1.2.3.4")
		h.Set("Forwarded", "for=1.2.3.4")
		h.Set("Accept", "*/*")
		return h
	}
	engine := NewEngine()
	engine.SetTrustedProxies([]string{"10.0.0.0/8"})

	untrusted := build()
	engine.StripUntrustedForwarding(untrusted, "203.0.113.9:1")
	if len(untrusted) != 1 || untrusted.Get("Accept") != "*/*" {
		t.Fatalf("untrusted peer: only non-forwarding headers may remain, got %#v", untrusted)
	}

	trustedHdr := build()
	engine.StripUntrustedForwarding(trustedHdr, "10.0.0.2:1")
	if !reflect.DeepEqual(trustedHdr, build()) {
		t.Fatalf("trusted peer: headers must be untouched, got %#v", trustedHdr)
	}

	none := build()
	NewEngine().StripUntrustedForwarding(none, "10.0.0.2:1")
	if len(none) != 1 {
		t.Fatalf("no trusted proxies configured: every peer is untrusted, got %#v", none)
	}
}

func TestExtractClientIPTrustedChain(t *testing.T) {
	trusted, err := config.ParseTrustedProxies([]string{"10.0.0.0/8", "172.16.0.5"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	header := func(xff ...string) http.Header {
		h := http.Header{}
		for _, v := range xff {
			h.Add("X-Forwarded-For", v)
		}
		return h
	}

	cases := []struct {
		name string
		ctx  RequestContext
		want string
	}{
		{"direct peer", RequestContext{RemoteAddr: "203.0.113.9:1", Header: header("1.2.3.4")}, "203.0.113.9"},
		{"trusted peer, single hop", RequestContext{RemoteAddr: "10.1.1.1:1", Header: header("198.51.100.1")}, "198.51.100.1"},
		{"trusted peer, multiple header lines", RequestContext{RemoteAddr: "172.16.0.5:1", Header: header("6.6.6.6", "198.51.100.1, 10.2.2.2")}, "198.51.100.1"},
		{"all hops trusted returns origin", RequestContext{RemoteAddr: "10.1.1.1:1", Header: header("10.5.5.5, 10.6.6.6")}, "10.5.5.5"},
		{"trusted peer, no XFF, X-Real-IP", RequestContext{RemoteAddr: "10.1.1.1:1", Header: http.Header{"X-Real-Ip": []string{"198.51.100.2"}}}, "198.51.100.2"},
		{"trusted peer, nothing forwarded", RequestContext{RemoteAddr: "10.1.1.1:1", Header: http.Header{}}, "10.1.1.1"},
	}
	for _, tc := range cases {
		if got := extractClientIP(tc.ctx, trusted); got != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}
