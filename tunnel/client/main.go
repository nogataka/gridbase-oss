package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/nogataka/gridbase-oss/tunnel/pkg/buildinfo"
	"github.com/nogataka/gridbase-oss/tunnel/pkg/config"
)

func main() {
	var (
		configPath      string
		serverAddr      string
		apiKey          string
		mode            string
		tunnelID        string
		httpOverrideURL string
		tcpHost         string
		tcpPort         int
		certPath        string
		keyPath         string
		caPath          string
		serverName      string
		useTLS          bool
		insecure        bool
		timeoutSec      int
	)

	flag.StringVar(&configPath, "config", "", "path to JSON or YAML client configuration file")
	flag.StringVar(&serverAddr, "server", "", "tunnel server address host:port (required unless set in the config file or GRIDBASE_CLIENT_SERVER_ADDR)")
	flag.StringVar(&serverAddr, "server_addr", "", "alias of --server")
	flag.StringVar(&apiKey, "api_key", "", "API key for authentication")
	flag.StringVar(&mode, "mode", config.DomainTypeHTTP, "tunnel mode (http or tcp)")
	flag.StringVar(&tunnelID, "tunnel_id", "", "tunnel identifier to bind in tcp mode")
	flag.StringVar(&httpOverrideURL, "local_server_url", "", "override local HTTP backend URL (defaults to the local_server_url registered for the subdomain)")
	flag.StringVar(&tcpHost, "tcp_host", "", "local host for tcp mode (e.g. 127.0.0.1)")
	flag.IntVar(&tcpPort, "tcp_port", 0, "local port for tcp mode")
	flag.BoolVar(&useTLS, "tls", false, "connect to the tunnel server over TLS (verified against system roots, or --ca)")
	flag.BoolVar(&insecure, "insecure", false, "connect over TLS but skip server certificate verification (development only)")
	flag.StringVar(&certPath, "cert", "", "client certificate path (only needed when the server requires mTLS; implies --tls)")
	flag.StringVar(&keyPath, "key", "", "client private key path (use together with --cert)")
	flag.StringVar(&caPath, "ca", "", "CA certificate file to trust instead of the system roots (implies --tls)")
	flag.StringVar(&serverName, "server_name", "", "expected server name for TLS verification (defaults to the host of --server)")
	flag.IntVar(&timeoutSec, "timeout", 30, "local HTTP request timeout seconds")
	flag.Parse()

	flagsSet := map[string]bool{}
	flag.Visit(func(f *flag.Flag) {
		flagsSet[f.Name] = true
	})

	inputs := CLIInputs{
		ConfigPath:      configPath,
		ServerAddr:      serverAddr,
		ServerName:      serverName,
		APIKey:          apiKey,
		Mode:            mode,
		TunnelID:        tunnelID,
		HTTPOverrideURL: httpOverrideURL,
		TCPLocalHost:    tcpHost,
		TCPLocalPort:    tcpPort,
		CertPath:        certPath,
		KeyPath:         keyPath,
		CAPath:          caPath,
		TLS:             useTLS,
		Insecure:        insecure,
		TimeoutSeconds:  timeoutSec,
		FlagsSet:        flagsSet,
	}

	cfg, err := loadClientConfig(inputs, os.LookupEnv)
	if err != nil {
		log.Fatalf("client configuration error: %v", err)
	}

	logger := log.New(os.Stdout, "[gridbase-client] ", log.LstdFlags|log.Lmicroseconds)
	info := buildinfo.Current()
	logger.Printf("build info version=%s commit=%s build_time=%s", info.Version, info.CommitSHA, info.BuildTime)
	metrics := NewClientMetrics()
	runner := NewRunner(logger, metrics)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
		defer signal.Stop(sigCh)
		select {
		case <-ctx.Done():
			return
		case sig := <-sigCh:
			logger.Printf("signal received signal=%s", sig)
			cancel()
		}
	}()

	if !cfg.TLS.UseTLS() {
		logger.Printf("warn: connecting WITHOUT TLS; the API key is sent in clear text. Use --tls (and --ca for a private CA) outside local development")
	} else if cfg.TLS.Insecure {
		logger.Printf("warn: --insecure set; server certificate is NOT verified")
	}
	if cfg.Mode == config.DomainTypeTCP {
		logger.Printf(
			"starting client: server_addr=%s server_name=%s mode=%s tunnel_id=%s local=%s:%d",
			cfg.ServerAddr,
			cfg.ServerName,
			cfg.Mode,
			cfg.TunnelID,
			cfg.TCP.LocalHost,
			cfg.TCP.LocalPort,
		)
	} else {
		logger.Printf("starting client: server_addr=%s server_name=%s mode=%s", cfg.ServerAddr, cfg.ServerName, cfg.Mode)
	}
	if cfg.Mode == config.DomainTypeHTTP && cfg.HTTP.OverrideURL != "" {
		logger.Printf("override local_server_url=%s", cfg.HTTP.OverrideURL)
	}
	if err := runner.Run(ctx, cfg); err != nil {
		if errors.Is(err, context.Canceled) {
			logger.Printf("client stopped (context cancelled)")
			return
		}
		logger.Fatalf("client exited with error: %v", err)
	}
	logger.Printf("client stopped gracefully")
}
