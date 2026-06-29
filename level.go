package logging

// Level represents the severity of a log entry.
//
// Levels are serialized to JSON as upper-case strings (e.g. "INFO"),
// mirroring the .NET Odeal.Logging LogLevel enum.
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

// severity returns a numeric ordering for the level so loggers can filter
// out entries below a configured minimum level.
func (l Level) severity() int {
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
	default:
		return 2
	}
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
