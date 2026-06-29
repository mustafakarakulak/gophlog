package logging

import (
	"sync"
)

var (
	defaultMu     sync.RWMutex
	defaultLogger = New()
)

// Default returns the package-level default logger.
func Default() *Logger {
	defaultMu.RLock()
	defer defaultMu.RUnlock()
	return defaultLogger
}

// SetDefault replaces the package-level default logger.
func SetDefault(l *Logger) {
	if l == nil {
		return
	}
	defaultMu.Lock()
	defaultLogger = l
	defaultMu.Unlock()
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

// Fatal starts a FATAL entry on the default logger.
func Fatal(message, event string) *Entry { return Default().Fatal(message, event) }
