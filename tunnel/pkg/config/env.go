package config

import (
	"fmt"
	"strconv"
	"strings"
)

// EnvProvider supplies environment variable values.
type EnvProvider func(string) (string, bool)

var envSetters = map[string]func(*Config, string) error{
	"GRIDBASE_BASE_DOMAIN": func(cfg *Config, value string) error {
		cfg.BaseDomain = value
		return nil
	},
	"GRIDBASE_TUNNEL_PORT": func(cfg *Config, value string) error {
		port, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid integer %q", value)
		}
		cfg.Tunnel.Port = port
		return nil
	},
	"GRIDBASE_HTTP_PORT": func(cfg *Config, value string) error {
		port, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid integer %q", value)
		}
		cfg.HTTP.Port = port
		return nil
	},
	"GRIDBASE_TUNNEL_MAX_FRAME_BYTES": func(cfg *Config, value string) error {
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid integer %q", value)
		}
		cfg.Tunnel.MaxFrameBytes = n
		return nil
	},
	"GRIDBASE_TUNNEL_HANDSHAKE_TIMEOUT": func(cfg *Config, value string) error {
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid integer %q", value)
		}
		cfg.Tunnel.HandshakeTimeout = n
		return nil
	},
	"GRIDBASE_HTTP_MAX_REQUEST_BODY_BYTES": func(cfg *Config, value string) error {
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid integer %q", value)
		}
		cfg.HTTP.MaxRequestBodyBytes = n
		return nil
	},
	"GRIDBASE_HTTP_MAX_RESPONSE_BODY_BYTES": func(cfg *Config, value string) error {
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid integer %q", value)
		}
		cfg.HTTP.MaxResponseBodyBytes = n
		return nil
	},
	"GRIDBASE_HTTP_TRUSTED_PROXIES": func(cfg *Config, value string) error {
		cfg.HTTP.TrustedProxies = nil
		for _, part := range strings.Split(value, ",") {
			if part = strings.TrimSpace(part); part != "" {
				cfg.HTTP.TrustedProxies = append(cfg.HTTP.TrustedProxies, part)
			}
		}
		return nil
	},
	"GRIDBASE_TLS_CERT": func(cfg *Config, value string) error {
		cfg.TLS.CertPath = value
		return nil
	},
	"GRIDBASE_TLS_KEY": func(cfg *Config, value string) error {
		cfg.TLS.KeyPath = value
		return nil
	},
	"GRIDBASE_CLIENT_CA": func(cfg *Config, value string) error {
		cfg.TLS.ClientCA = value
		return nil
	},
	"GRIDBASE_ADMIN_BEARER_TOKEN": func(cfg *Config, value string) error {
		cfg.Admin.BearerToken = value
		return nil
	},
	"GRIDBASE_ADMIN_HOST": func(cfg *Config, value string) error {
		cfg.Admin.Host = value
		return nil
	},
	"GRIDBASE_ADMIN_CORS_ORIGIN": func(cfg *Config, value string) error {
		cfg.Admin.CORSOrigin = value
		return nil
	},
	"GRIDBASE_ADMIN_TIMEZONE": func(cfg *Config, value string) error {
		cfg.Admin.Timezone = value
		return nil
	},
	"GRIDBASE_DIRECTUS_URL": func(cfg *Config, value string) error {
		cfg.Directus.URL = value
		return nil
	},
	"GRIDBASE_DIRECTUS_TOKEN": func(cfg *Config, value string) error {
		cfg.Directus.Token = value
		return nil
	},
}

func applyEnvOverrides(cfg *Config, lookup EnvProvider) error {
	if lookup == nil {
		return nil
	}

	var issues []string
	for key, setter := range envSetters {
		if raw, ok := lookup(key); ok {
			value := strings.TrimSpace(raw)
			if value == "" {
				continue
			}
			if err := setter(cfg, value); err != nil {
				issues = append(issues, fmt.Sprintf("%s: %v", key, err))
			}
		}
	}

	if len(issues) > 0 {
		return &ValidationError{Issues: issues}
	}
	return nil
}
