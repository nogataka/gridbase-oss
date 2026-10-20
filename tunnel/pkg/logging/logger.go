package logging

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// Level represents the severity of a log entry.
type Level int

const (
	// LevelDebug emits diagnostic information useful for development.
	LevelDebug Level = iota
	// LevelInfo records high-level operational events.
	LevelInfo
	// LevelWarn highlights unexpected but non-fatal events.
	LevelWarn
	// LevelError captures failures requiring attention.
	LevelError
)

// Fields holds structured key/value pairs attached to log entries.
type Fields map[string]any

// Option customises logger construction.
type Option func(*Logger)

// Logger emits structured JSON log entries with shared contextual fields.
type Logger struct {
	mu       *sync.Mutex
	out      io.Writer
	base     Fields
	minLevel Level
	clock    func() time.Time
}

// New constructs a structured logger scoped to the supplied component name.
func New(component string, opts ...Option) *Logger {
	l := &Logger{
		mu:       &sync.Mutex{},
		out:      os.Stdout,
		base:     Fields{},
		minLevel: LevelInfo,
		clock:    time.Now,
	}
	if component != "" {
		l.base["component"] = component
	}
	for _, opt := range opts {
		if opt != nil {
			opt(l)
		}
	}
	return l
}

// WithWriter routes log entries to the provided writer.
func WithWriter(w io.Writer) Option {
	return func(l *Logger) {
		if w != nil {
			l.out = w
		}
	}
}

// WithLevel sets the minimum severity level.
func WithLevel(level Level) Option {
	return func(l *Logger) {
		if level >= LevelDebug && level <= LevelError {
			l.minLevel = level
		}
	}
}

// WithFields pre-populates the logger with additional base fields.
func WithFields(fields Fields) Option {
	return func(l *Logger) {
		if len(fields) == 0 {
			return
		}
		for k, v := range fields {
			if k == "" {
				continue
			}
			l.base[k] = v
		}
	}
}

// WithClock injects a deterministic clock for testing.
func WithClock(clock func() time.Time) Option {
	return func(l *Logger) {
		if clock != nil {
			l.clock = clock
		}
	}
}

// With clones the logger, augmenting it with additional contextual fields.
func (l *Logger) With(fields Fields) *Logger {
	merged := make(Fields, len(l.base)+len(fields))
	for k, v := range l.base {
		merged[k] = v
	}
	for k, v := range fields {
		if k == "" {
			continue
		}
		merged[k] = v
	}
	return &Logger{
		mu:       l.mu,
		out:      l.out,
		base:     merged,
		minLevel: l.minLevel,
		clock:    l.clock,
	}
}

// WithField returns a cloned logger with a single additional field.
func (l *Logger) WithField(key string, value any) *Logger {
	if key == "" {
		return l
	}
	return l.With(Fields{key: value})
}

// Debug logs a debug entry.
func (l *Logger) Debug(msg string, fields Fields) {
	l.log(LevelDebug, msg, fields)
}

// Debugf formats and logs a debug entry.
func (l *Logger) Debugf(format string, args ...any) {
	l.log(LevelDebug, fmt.Sprintf(format, args...), nil)
}

// Info logs an informational entry.
func (l *Logger) Info(msg string, fields Fields) {
	l.log(LevelInfo, msg, fields)
}

// Infof formats and logs an informational entry.
func (l *Logger) Infof(format string, args ...any) {
	l.log(LevelInfo, fmt.Sprintf(format, args...), nil)
}

// Warn logs a warning entry.
func (l *Logger) Warn(msg string, fields Fields) {
	l.log(LevelWarn, msg, fields)
}

// Warnf formats and logs a warning entry.
func (l *Logger) Warnf(format string, args ...any) {
	l.log(LevelWarn, fmt.Sprintf(format, args...), nil)
}

// Error logs an error entry with optional fields.
func (l *Logger) Error(msg string, err error, fields Fields) {
	merged := cloneFields(fields)
	if err != nil {
		merged["error"] = err.Error()
	}
	l.log(LevelError, msg, merged)
}

// Errorf formats and logs an error entry without a stack trace.
func (l *Logger) Errorf(format string, args ...any) {
	l.log(LevelError, fmt.Sprintf(format, args...), nil)
}

func (l *Logger) log(level Level, msg string, fields Fields) {
	if level < l.minLevel {
		return
	}
	if fields == nil {
		fields = Fields{}
	}
	record := make(Fields, len(l.base)+len(fields)+3)
	for k, v := range l.base {
		record[k] = v
	}
	for k, v := range fields {
		if k == "" {
			continue
		}
		record[k] = v
	}
	record["ts"] = l.clock().UTC().Format(time.RFC3339Nano)
	record["level"] = level.String()
	if msg != "" {
		record["msg"] = msg
	}

	payload, err := json.Marshal(record)
	if err != nil {
		fallback := fmt.Sprintf(`{"ts":"%s","level":"error","msg":"logger marshal failed","error":%q}`,
			l.clock().UTC().Format(time.RFC3339Nano), err.Error())
		l.mu.Lock()
		_, _ = l.out.Write([]byte(fallback + "\n"))
		l.mu.Unlock()
		return
	}

	l.mu.Lock()
	_, _ = l.out.Write(payload)
	_, _ = l.out.Write([]byte("\n"))
	l.mu.Unlock()
}

func cloneFields(fields Fields) Fields {
	if len(fields) == 0 {
		return Fields{}
	}
	cloned := make(Fields, len(fields))
	for k, v := range fields {
		if k == "" {
			continue
		}
		cloned[k] = v
	}
	return cloned
}

// MergeFields returns a shallow copy of base with additional field maps merged in order.
// Later maps override conflicting keys while preserving the original base map.
func MergeFields(base Fields, extras ...Fields) Fields {
	merged := cloneFields(base)
	for _, extra := range extras {
		if len(extra) == 0 {
			continue
		}
		for k, v := range extra {
			if k == "" {
				continue
			}
			merged[k] = v
		}
	}
	return merged
}

// String returns canonical string representation for the level.
func (l Level) String() string {
	switch l {
	case LevelDebug:
		return "debug"
	case LevelInfo:
		return "info"
	case LevelWarn:
		return "warn"
	case LevelError:
		return "error"
	default:
		return "unknown"
	}
}
