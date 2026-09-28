package gophlog

import "strings"

// Level represents the severity of a log entry.
//
// Levels are serialized to JSON as upper-case strings (e.g. "INFO"). Level
// names are matched case-insensitively, so Level("error") is ERROR; any other
// value is written as given and filtered as INFO.
type Level string

const (
	// TRACE is the most detailed log level.
	TRACE Level = "TRACE"
	// DEBUG is used for debugging information.
	DEBUG Level = "DEBUG"
	// INFO is used for informational messages.
	INFO Level = "INFO"
	// WARN is used for warning messages (e.g. 4xx, retries).
	WARN Level = "WARN"
	// ERROR is used for errors (exceptions, failed operations).
	ERROR Level = "ERROR"
	// FATAL is used for critical errors.
	FATAL Level = "FATAL"
)

// levelsBySeverity lists the levels in severity order; a level's index is its
// severity.
var levelsBySeverity = [...]Level{TRACE, DEBUG, INFO, WARN, ERROR, FATAL}

// severity returns a numeric ordering for the level so loggers can filter
// out entries below a configured minimum level.
func (l Level) severity() int {
	_, sev := l.resolve()
	return sev
}

// canonicalSeverity is the call-free fast path of resolve: the severity of a
// canonically spelled level, or -1 for any other value. It stays cheap enough
// for newEntry, and with it the entry points, to be inlined.
func (l Level) canonicalSeverity() int {
	switch l {
	case TRACE:
		return 0
	case DEBUG:
		return 1
	case INFO:
		return 2
	case WARN:
		return 3
	case ERROR:
		return 4
	case FATAL:
		return 5
	}
	return -1
}

// resolve returns the level to write for l and its severity. Level names match
// case-insensitively, so Level("error") filters and is written as ERROR; any
// other value keeps its own spelling and filters as INFO.
func (l Level) resolve() (Level, int) {
	if sev := l.canonicalSeverity(); sev >= 0 {
		return l, sev
	}
	for sev, lv := range levelsBySeverity {
		if strings.EqualFold(string(l), string(lv)) {
			return lv, sev
		}
	}
	return l, 2
}

// LogType categorizes a log entry. It is serialized as a lower-case string.
type LogType string

const (
	// LogTypeApp is the default application log type.
	LogTypeApp LogType = "app"
	// LogTypeAudit marks audit logs.
	LogTypeAudit LogType = "audit"
	// LogTypeSecurity marks security logs.
	LogTypeSecurity LogType = "security"
)

// IntegrationStatus describes the outcome of an external integration call.
// It is serialized as a lower-case string.
type IntegrationStatus string

const (
	// IntegrationSuccess indicates a successful call.
	IntegrationSuccess IntegrationStatus = "success"
	// IntegrationFail indicates a failed call.
	IntegrationFail IntegrationStatus = "fail"
	// IntegrationTimeout indicates the call timed out.
	IntegrationTimeout IntegrationStatus = "timeout"
	// IntegrationRetry indicates the call is being retried.
	IntegrationRetry IntegrationStatus = "retry"
)
