// Package logging provides the minimal leveled logger used across Free-Model-Router.
package logging

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// Level is a log severity.
type Level int

const (
	Debug Level = iota
	Info
	Warn
	Error
)

var levelNames = map[Level]string{
	Debug: "DEBUG",
	Info:  "INFO",
	Warn:  "WARN",
	Error: "ERROR",
}

// ParseLevel maps a config string to a Level.
func ParseLevel(s string) (Level, error) {
	switch strings.ToLower(s) {
	case "debug":
		return Debug, nil
	case "info", "":
		return Info, nil
	case "warn":
		return Warn, nil
	case "error":
		return Error, nil
	default:
		return Info, fmt.Errorf("unknown log level %q", s)
	}
}

// Logger is a leveled, mutex-guarded logger. Never log secrets (PLAN_V7 §11/§14).
type Logger struct {
	mu    sync.Mutex
	w     io.Writer
	min   Level
	level func() Level // dynamic minimum level
}

// New creates a Logger writing to w at minimum level min.
func New(min Level, w io.Writer) (*Logger, error) {
	if w == nil {
		w = os.Stderr
	}
	return &Logger{w: w, min: min}, nil
}

// Debugf logs at debug level.
func (l *Logger) Debugf(format string, args ...any) { l.logf(Debug, format, args...) }

// Infof logs at info level.
func (l *Logger) Infof(format string, args ...any) { l.logf(Info, format, args...) }

// Warnf logs at warn level.
func (l *Logger) Warnf(format string, args ...any) { l.logf(Warn, format, args...) }

// Errorf logs at error level.
func (l *Logger) Errorf(format string, args ...any) { l.logf(Error, format, args...) }

// Convenience aliases matching the call sites in cmd/Free-Model-Router and app.
func (l *Logger) Debug(format string, args ...any) { l.logf(Debug, format, args...) }
func (l *Logger) Info(format string, args ...any)  { l.logf(Info, format, args...) }
func (l *Logger) Warn(format string, args ...any)  { l.logf(Warn, format, args...) }
func (l *Logger) Error(format string, args ...any) { l.logf(Error, format, args...) }

// Close flushes; nothing to flush for unbuffered writers.
func (l *Logger) Close() error { return nil }

func (l *Logger) logf(lv Level, format string, args ...any) {
	if lv < l.min {
		return
	}
	msg := fmt.Sprintf(format, args...)
	line := fmt.Sprintf("%s [%s] %s\n", time.Now().Format("2006-01-02T15:04:05Z07:00"), levelNames[lv], msg)
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = io.WriteString(l.w, line)
}
