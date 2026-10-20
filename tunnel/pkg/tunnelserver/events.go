package tunnelserver

import (
	"strings"
	"unicode/utf8"

	"github.com/nogataka/gridbase-oss/tunnel/pkg/logging"
)

// eventRecorder provides structured logging helpers with consistent event fields.
type eventRecorder struct {
	logger *logging.Logger
	base   logging.Fields
}

func newEventRecorder(logger *logging.Logger) *eventRecorder {
	if logger == nil {
		logger = logging.New("tunnel-server")
	}
	return &eventRecorder{logger: logger, base: logging.Fields{}}
}

// With returns a recorder that automatically attaches the provided fields to every log entry.
func (r *eventRecorder) With(fields logging.Fields) *eventRecorder {
	if r == nil {
		return nil
	}
	merged := logging.MergeFields(r.base, fields)
	return &eventRecorder{logger: r.logger, base: merged}
}

// WithTunnel returns a recorder whose entries include tunnel metadata.
func (r *eventRecorder) WithTunnel(mode, tunnelID string) *eventRecorder {
	extra := logging.Fields{}
	if mode != "" {
		extra["mode"] = mode
	}
	if tunnelID != "" {
		extra["tunnel_id"] = tunnelID
	}
	return r.With(extra)
}

// Debug records a debug-level event.
func (r *eventRecorder) Debug(event, msg string, fields logging.Fields) {
	r.log(logging.LevelDebug, event, msg, nil, fields)
}

// Info records an informational event.
func (r *eventRecorder) Info(event, msg string, fields logging.Fields) {
	r.log(logging.LevelInfo, event, msg, nil, fields)
}

// Warn records a warning event, optionally attaching an error.
func (r *eventRecorder) Warn(event, msg string, err error, fields logging.Fields) {
	r.log(logging.LevelWarn, event, msg, err, fields)
}

// Error records an error event.
func (r *eventRecorder) Error(event, msg string, err error, fields logging.Fields) {
	r.log(logging.LevelError, event, msg, err, fields)
}

func (r *eventRecorder) log(level logging.Level, event, msg string, err error, fields logging.Fields) {
	if r.logger == nil {
		return
	}
	payload := logging.MergeFields(r.base, r.copyFields(fields))
	payload["event"] = event
	switch level {
	case logging.LevelDebug:
		r.logger.Debug(msg, payload)
	case logging.LevelInfo:
		r.logger.Info(msg, payload)
	case logging.LevelWarn:
		if err != nil {
			payload["error"] = err.Error()
		}
		r.logger.Warn(msg, payload)
	case logging.LevelError:
		r.logger.Error(msg, err, payload)
	default:
		r.logger.Info(msg, payload)
	}
}

func (r *eventRecorder) withTunnelContext(mode, tunnelID string, fields logging.Fields) logging.Fields {
	extra := r.copyFields(fields)
	// mode and tunnel_id can come straight from an unauthenticated hello (up to 64 KiB),
	// so they are always cut to a short length before they reach a log line.
	if mode != "" {
		extra["mode"] = truncateForLog(mode)
	}
	if tunnelID != "" {
		extra["tunnel_id"] = truncateForLog(tunnelID)
	}
	return logging.MergeFields(r.base, extra)
}

// maxLogFieldBytes bounds attacker-controlled strings (mode, tunnel_id) in log fields.
const maxLogFieldBytes = 64

// truncateForLog shortens s to at most maxLogFieldBytes bytes (plus a marker), never
// splitting a UTF-8 sequence and never emitting invalid UTF-8.
func truncateForLog(s string) string {
	if len(s) <= maxLogFieldBytes {
		return strings.ToValidUTF8(s, "?")
	}
	cut := maxLogFieldBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return strings.ToValidUTF8(s[:cut], "?") + "...(truncated)"
}

func (r *eventRecorder) copyFields(fields logging.Fields) logging.Fields {
	if len(fields) == 0 {
		return logging.Fields{}
	}
	clone := logging.Fields{}
	for k, v := range fields {
		if k == "" {
			continue
		}
		clone[k] = v
	}
	return clone
}
