package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nogataka/gridbase-oss/tunnel/protocol"
)

// selfSigned generates a throw-away self-signed server certificate for 127.0.0.1/localhost.
func selfSigned(t *testing.T) (tls.Certificate, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "localhost"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	caPath := filepath.Join(t.TempDir(), "ca.crt")
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, caPath
}

// serveHello accepts connections and answers the handshake with an OK ServerHello.
func serveHello(t *testing.T, ln net.Listener) {
	t.Helper()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				var hello protocol.ClientHello
				if err := protocol.ReadMessage(c, &hello); err != nil {
					return
				}
				_ = protocol.WriteMessage(c, protocol.ServerHello{OK: hello.APIKey == "k1", Message: "registered", Mode: hello.Mode})
				time.Sleep(200 * time.Millisecond)
			}(conn)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
}

func testRunner() *Runner {
	return NewRunner(log.New(io.Discard, "", 0), NewClientMetrics())
}

func baseCfg(addr string) *ClientConfig {
	cfg := defaultClientConfig()
	cfg.ServerAddr = addr
	cfg.APIKey = "k1"
	cfg.normalize()
	return cfg
}

func TestEstablishConnectionPlain(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serveHello(t, ln)

	cfg := baseCfg(ln.Addr().String())
	if cfg.TLS.UseTLS() {
		t.Fatal("TLS must be off by default")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, hello, err := testRunner().establishConnection(ctx, cfg)
	if err != nil {
		t.Fatalf("plain connection failed: %v", err)
	}
	defer conn.Close()
	if !hello.OK {
		t.Fatalf("unexpected hello %+v", hello)
	}
}

func TestEstablishConnectionTLSWithCAAndInsecure(t *testing.T) {
	cert, caPath := selfSigned(t)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatal(err)
	}
	serveHello(t, ln)
	addr := ln.Addr().String()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// --ca trusts the private CA; no client certificate is needed.
	cfg := baseCfg(addr)
	cfg.TLS.CAPath = caPath
	cfg.ServerName = "localhost"
	conn, _, err := testRunner().establishConnection(ctx, cfg)
	if err != nil {
		t.Fatalf("tls with --ca failed: %v", err)
	}
	conn.Close()

	// --insecure skips verification.
	cfg = baseCfg(addr)
	cfg.TLS.Insecure = true
	conn, _, err = testRunner().establishConnection(ctx, cfg)
	if err != nil {
		t.Fatalf("tls with --insecure failed: %v", err)
	}
	conn.Close()

	// --tls alone verifies against system roots and must reject the self-signed server.
	cfg = baseCfg(addr)
	cfg.TLS.Enabled = true
	if conn, _, err = testRunner().establishConnection(ctx, cfg); err == nil {
		conn.Close()
		t.Fatal("expected verification failure for unknown CA")
	}
}

func TestBuildTLSConfig(t *testing.T) {
	_, caPath := selfSigned(t)

	cfg := baseCfg("127.0.0.1:1")
	cfg.TLS.Enabled = true
	tc, err := buildTLSConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if tc.RootCAs != nil || len(tc.Certificates) != 0 || tc.InsecureSkipVerify {
		t.Fatalf("--tls alone must use system roots, no client cert, verification on: %+v", tc)
	}

	cfg.TLS.CAPath = caPath
	if tc, err = buildTLSConfig(cfg); err != nil || tc.RootCAs == nil {
		t.Fatalf("expected custom roots, err=%v", err)
	}

	cfg = baseCfg("127.0.0.1:1")
	cfg.TLS.CAPath = filepath.Join(t.TempDir(), "missing.crt")
	if _, err = buildTLSConfig(cfg); err == nil {
		t.Fatal("expected error for missing CA file")
	}

	cfg = baseCfg("127.0.0.1:1")
	cfg.TLS.CertPath = "only-cert.pem"
	if _, err = buildTLSConfig(cfg); err == nil {
		t.Fatal("expected error for cert without key")
	}
}

func TestUseTLSDerivation(t *testing.T) {
	cases := map[string]TLSFiles{
		"enabled":  {Enabled: true},
		"insecure": {Insecure: true},
		"ca":       {CAPath: "ca.pem"},
		"cert":     {CertPath: "c.pem"},
	}
	for name, f := range cases {
		if !f.UseTLS() {
			t.Fatalf("%s should imply TLS", name)
		}
	}
	if (TLSFiles{}).UseTLS() {
		t.Fatal("zero value must be plain")
	}
}

func TestLoadClientConfigRequiresServer(t *testing.T) {
	_, err := loadClientConfig(CLIInputs{APIKey: "k", FlagsSet: map[string]bool{"api_key": true}}, nil)
	if err == nil {
		t.Fatal("expected error when no server address is configured (no built-in default)")
	}
}

func TestLoadClientConfigServerFlagAndTLSFlags(t *testing.T) {
	inputs := CLIInputs{
		ServerAddr: "tunnel.example.com:9443",
		APIKey:     "k",
		Mode:       "http",
		TLS:        true,
		FlagsSet:   map[string]bool{"server": true, "api_key": true, "tls": true},
	}
	cfg, err := loadClientConfig(inputs, nil)
	if err != nil {
		t.Fatalf("loadClientConfig: %v", err)
	}
	if cfg.ServerAddr != "tunnel.example.com:9443" || cfg.ServerName != "tunnel.example.com" {
		t.Fatalf("unexpected server settings: %q / %q", cfg.ServerAddr, cfg.ServerName)
	}
	if !cfg.TLS.UseTLS() {
		t.Fatal("--tls must enable TLS")
	}

	// Environment variables select the server and TLS too.
	env := map[string]string{envServerAddr: "10.0.0.5:8000", envTLSInsecure: "true"}
	cfg, err = loadClientConfig(CLIInputs{APIKey: "k", FlagsSet: map[string]bool{"api_key": true}}, func(k string) (string, bool) {
		v, ok := env[k]
		return v, ok
	})
	if err != nil {
		t.Fatalf("loadClientConfig(env): %v", err)
	}
	if !cfg.TLS.Insecure || cfg.ServerAddr != "10.0.0.5:8000" {
		t.Fatalf("env not applied: %+v", cfg)
	}
}

func TestClientValidateTLSPairs(t *testing.T) {
	cfg := baseCfg("127.0.0.1:1")
	cfg.TLS.CertPath = "/nonexistent"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for cert without key")
	}
	cfg = baseCfg("127.0.0.1:1")
	cfg.TLS.Insecure = true
	cfg.TLS.CAPath = "ca.pem"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error: insecure and ca are mutually exclusive")
	}
}
