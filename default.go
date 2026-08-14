package gophlog

import (
	"sync/atomic"
)

// defaultLogger is stored atomically so the package-level helpers resolve it
// with a single atomic load instead of taking a lock on every log call.
var defaultLogger atomic.Pointer[Logger]

func init() { defaultLogger.Store(New()) }

// Default returns the package-level default logger.
func Default() *Logger { return defaultLogger.Load() }

// SetDefault replaces the package-level default logger. A nil logger is ignored.
func SetDefault(l *Logger) {
	if l == nil {
		return
	}
	defaultLogger.Store(l)
}

// Trace starts a TRACE entry on the default logger.
func Trace(message, event string) *Entry { return Default().Trace(message, event) }

// Debug starts a DEBUG entry on the default logger.
func Debug(message, event string) *Entry { return Default().Debug(message, event) }

// Info starts an INFO entry on the default logger.
func Info(message, event string) *Entry { return Default().Info(message, event) }

// Warn starts a WARN entry on the default logger.
func Warn(message, event string) *Entry { return Default().Warn(message, event) }

// Error starts an ERROR entry on the default logger.
func Error(message, event string) *Entry { return Default().Error(message, event) }

// Fatal starts a FATAL entry on the default logger. It does NOT terminate the
// process; the caller decides whether to exit after gophlog.
func Fatal(message, event string) *Entry { return Default().Fatal(message, event) }
