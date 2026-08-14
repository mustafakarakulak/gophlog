package gophlog

import "strings"

// boundFields holds the fields bound to a derived logger via With. Bound
// values are the weakest defaults: an explicit Entry value or a context value
// always wins over them.
type boundFields struct {
	logType  LogType
	category string

	tenantID  string
	userID    string
	clientIP  string
	sessionID string
	requestID string

	integration *IntegrationInfo
	queue       *QueueInfo
	job         *JobInfo

	extra          map[string]any
	maskStrategies map[string]MaskingStrategy
}

// emptyBound is used when a logger has no bound fields, so build can read
// fields without a nil check.
var emptyBound boundFields

func (b *boundFields) clone() *boundFields {
	c := *b
	if len(b.extra) > 0 {
		c.extra = make(map[string]any, len(b.extra))
		for k, v := range b.extra {
			c.extra[k] = v
		}
	}
	if len(b.maskStrategies) > 0 {
		c.maskStrategies = make(map[string]MaskingStrategy, len(b.maskStrategies))
		for k, v := range b.maskStrategies {
			c.maskStrategies[k] = v
		}
	}
	return &c
}

// With returns a builder for deriving a child logger with pre-bound fields, so
// values shared by many log entries (module category, tenant, static extras,
// masking rules) are attached once instead of on every call:
//
//	payLog := log.With().Category("payments").Tenant("acme").Logger()
//	payLog.Info("charged", "payment_charged").Log()
//
// The child shares the parent's writer, minimum level and other core
// configuration. Binding starts from the parent's own bound fields, so
// children can be derived from children.
func (l *Logger) With() *With {
	w := &With{core: l.core}
	if l.bound != nil {
		w.bound = l.bound.clone()
	} else {
		w.bound = &boundFields{}
	}
	return w
}

// With is a fluent builder that accumulates fields for a derived logger.
// Finish with Logger(). A With is not safe for concurrent use; build it from
// one goroutine.
type With struct {
	core  *loggerCore
	bound *boundFields
}

// Logger returns the derived logger carrying the accumulated fields. The
// builder can keep being extended afterwards to derive further loggers; the
// returned logger is unaffected.
func (w *With) Logger() *Logger {
	return &Logger{core: w.core, bound: w.bound.clone()}
}

// LogType binds the log type (app/audit/security).
func (w *With) LogType(t LogType) *With { w.bound.logType = t; return w }

// Category binds the log category.
func (w *With) Category(category string) *With { w.bound.category = category; return w }

// Tenant binds the tenant ID.
func (w *With) Tenant(id string) *With { w.bound.tenantID = id; return w }

// User binds the user ID.
func (w *With) User(id string) *With { w.bound.userID = id; return w }

// ClientIP binds the client IP.
func (w *With) ClientIP(ip string) *With { w.bound.clientIP = ip; return w }

// Session binds the session ID.
func (w *With) Session(id string) *With { w.bound.sessionID = id; return w }

// RequestID binds the request ID.
func (w *With) RequestID(id string) *With { w.bound.requestID = id; return w }

// Integration binds a default IntegrationInfo, used when an entry sets none.
func (w *With) Integration(info *IntegrationInfo) *With { w.bound.integration = info; return w }

// Queue binds a default QueueInfo, used when an entry sets none.
func (w *With) Queue(info *QueueInfo) *With { w.bound.queue = info; return w }

// Job binds a default JobInfo, used when an entry sets none.
func (w *With) Job(info *JobInfo) *With { w.bound.job = info; return w }

// Extra merges a map of searchable extra fields. Entry-level and payload
// logextra fields with the same key override bound ones.
func (w *With) Extra(extra map[string]any) *With {
	if len(extra) == 0 {
		return w
	}
	if w.bound.extra == nil {
		w.bound.extra = make(map[string]any, len(extra))
	}
	for k, v := range extra {
		w.bound.extra[k] = v
	}
	return w
}

// ExtraField binds a single searchable extra field.
func (w *With) ExtraField(key string, value any) *With {
	if w.bound.extra == nil {
		w.bound.extra = make(map[string]any, 1)
	}
	w.bound.extra[key] = value
	return w
}

// Mask binds a payload masking strategy for a field (case-insensitive), applied
// to every entry logged through the derived logger. Entry-level strategies for
// the same field override it.
func (w *With) Mask(field string, strategy MaskingStrategy) *With {
	if w.bound.maskStrategies == nil {
		w.bound.maskStrategies = make(map[string]MaskingStrategy)
	}
	// Lower-cased on insert, mirroring Entry.addMask, so emit never rebuilds
	// the lookup map.
	w.bound.maskStrategies[strings.ToLower(field)] = strategy
	return w
}

// MaskMany binds masking strategies for multiple payload fields.
func (w *With) MaskMany(strategies map[string]MaskingStrategy) *With {
	for k, v := range strategies {
		w.Mask(k, v)
	}
	return w
}
