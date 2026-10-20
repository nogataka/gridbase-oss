package tunnelserver

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
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nogataka/gridbase-oss/tunnel/pkg/config"
	"github.com/nogataka/gridbase-oss/tunnel/pkg/logging"
	"github.com/nogataka/gridbase-oss/tunnel/protocol"
)

// testPKI holds throw-away certificates generated at test time (nothing is committed).
type testPKI struct {
	caPath, serverCertPath, serverKeyPath string
	clientCert                            tls.Certificate
	caPool                                *x509.CertPool
}

func writePEM(t *testing.T, path, blockType string, der []byte) {
	t.Helper()
	data := pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der})
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func newTestPKI(t *testing.T) *testPKI {
	t.Helper()
	dir := t.TempDir()

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}

	issue := func(cn string, usage x509.ExtKeyUsage, serial int64) (certDER []byte, key *ecdsa.PrivateKey) {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		tmpl := &x509.Certificate{
			SerialNumber: big.NewInt(serial),
			Subject:      pkix.Name{CommonName: cn},
			NotBefore:    time.Now().Add(-time.Hour),
			NotAfter:     time.Now().Add(24 * time.Hour),
			KeyUsage:     x509.KeyUsageDigitalSignature,
			ExtKeyUsage:  []x509.ExtKeyUsage{usage},
			DNSNames:     []string{"localhost"},
			IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		}
		certDER, err = x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
		if err != nil {
			t.Fatal(err)
		}
		return certDER, key
	}

	srvDER, srvKey := issue("localhost", x509.ExtKeyUsageServerAuth, 2)
	cliDER, cliKey := issue("test-client", x509.ExtKeyUsageClientAuth, 3)

	pki := &testPKI{
		caPath:         filepath.Join(dir, "ca.crt"),
		serverCertPath: filepath.Join(dir, "server.crt"),
		serverKeyPath:  filepath.Join(dir, "server.key"),
		caPool:         x509.NewCertPool(),
	}
	pki.caPool.AddCert(caCert)
	writePEM(t, pki.caPath, "CERTIFICATE", caDER)
	writePEM(t, pki.serverCertPath, "CERTIFICATE", srvDER)
	srvKeyDER, err := x509.MarshalECPrivateKey(srvKey)
	if err != nil {
		t.Fatal(err)
	}
	writePEM(t, pki.serverKeyPath, "EC PRIVATE KEY", srvKeyDER)

	pki.clientCert = tls.Certificate{Certificate: [][]byte{cliDER}, PrivateKey: cliKey}
	return pki
}

func startServer(t *testing.T, tlsCfg config.TLSConfig) *TunnelServer {
	t.Helper()
	cfg := &config.Config{
		BaseDomain: "gridbase.test",
		TLS:        tlsCfg,
		Tunnel:     config.TunnelConfig{Port: 0},
		Domains: []config.DomainConfig{
			{APIKey: "k1", Subdomain: "web", Type: config.DomainTypeHTTP, LocalURL: "http://127.0.0.1:1"},
		},
	}
	ts, err := NewTunnelServer(cfg, logging.New("tls-test", logging.WithWriter(io.Discard)))
	if err != nil {
		t.Fatalf("NewTunnelServer: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = ts.AcceptLoop(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		_ = ts.Close()
		<-done
	})
	return ts
}

func handshake(t *testing.T, conn net.Conn, apiKey string) protocol.ServerHello {
	t.Helper()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if err := protocol.WriteMessage(conn, protocol.ClientHello{APIKey: apiKey, Mode: config.DomainTypeHTTP}); err != nil {
		t.Fatalf("write hello: %v", err)
	}
	var resp protocol.ServerHello
	if err := protocol.ReadMessage(conn, &resp); err != nil {
		t.Fatalf("read hello: %v", err)
	}
	return resp
}

func TestTunnelListenerPlainWhenNoTLSConfigured(t *testing.T) {
	ts := startServer(t, config.TLSConfig{})

	conn, err := net.Dial("tcp", ts.ListenerAddr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if resp := handshake(t, conn, "k1"); !resp.OK {
		t.Fatalf("expected handshake OK on plain listener, got %+v", resp)
	}
}

func TestTunnelListenerTLSWithoutClientCertRequirement(t *testing.T) {
	pki := newTestPKI(t)
	ts := startServer(t, config.TLSConfig{CertPath: pki.serverCertPath, KeyPath: pki.serverKeyPath})

	conn, err := tls.Dial("tcp", ts.ListenerAddr().String(), &tls.Config{RootCAs: pki.caPool, ServerName: "localhost"})
	if err != nil {
		t.Fatalf("tls dial without client cert must succeed when client_ca is unset: %v", err)
	}
	defer conn.Close()

	if resp := handshake(t, conn, "k1"); !resp.OK {
		t.Fatalf("expected handshake OK, got %+v", resp)
	}
}

func TestTunnelListenerMTLSWhenClientCAConfigured(t *testing.T) {
	pki := newTestPKI(t)
	ts := startServer(t, config.TLSConfig{CertPath: pki.serverCertPath, KeyPath: pki.serverKeyPath, ClientCA: pki.caPath})
	addr := ts.ListenerAddr().String()

	// Without a client certificate the connection must be refused (TLS 1.3 surfaces
	// the alert on the first read, so the hello exchange has to fail).
	conn, err := tls.Dial("tcp", addr, &tls.Config{RootCAs: pki.caPool, ServerName: "localhost"})
	if err == nil {
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		werr := protocol.WriteMessage(conn, protocol.ClientHello{APIKey: "k1"})
		var resp protocol.ServerHello
		rerr := protocol.ReadMessage(conn, &resp)
		conn.Close()
		if werr == nil && rerr == nil {
			t.Fatalf("handshake without client cert unexpectedly succeeded: %+v", resp)
		}
	}

	// With a CA-signed client certificate it works.
	conn, err = tls.Dial("tcp", addr, &tls.Config{
		RootCAs:      pki.caPool,
		ServerName:   "localhost",
		Certificates: []tls.Certificate{pki.clientCert},
	})
	if err != nil {
		t.Fatalf("tls dial with client cert: %v", err)
	}
	defer conn.Close()
	if resp := handshake(t, conn, "k1"); !resp.OK {
		t.Fatalf("expected handshake OK with client cert, got %+v", resp)
	}
}

func TestBaseDomainFollowsReload(t *testing.T) {
	ts := startServer(t, config.TLSConfig{})
	if got := ts.BaseDomain(); got != "gridbase.test" {
		t.Fatalf("unexpected base domain %q", got)
	}
	ts.Reload(&config.Config{BaseDomain: "other.example.com"})
	if got := ts.BaseDomain(); got != "other.example.com" {
		t.Fatalf("base domain not updated on reload: %q", got)
	}
}
