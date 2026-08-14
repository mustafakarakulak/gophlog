package gophlog

import "encoding/json"

// Event is the structured log record emitted as a single line of JSON.
//
// Optional fields are omitted when empty so the output stays clean
// ("null-safe").
//
// Note: payload is serialized as a *stringified* JSON value (a JSON string
// whose content is itself JSON). The `extra` object, by contrast, is emitted as
// real nested JSON so it stays searchable in OpenSearch.
type Event struct {
	Timestamp string  `json:"timestamp"`
	Level     Level   `json:"level"`
	LogType   LogType `json:"log_type,omitempty"`
	Category  string  `json:"category,omitempty"`

	TraceID   string `json:"trace_id,omitempty"`
	SpanID    string `json:"span_id,omitempty"`
	RequestID string `json:"request_id,omitempty"`

	TenantID  string `json:"tenant_id,omitempty"`
	UserID    string `json:"user_id,omitempty"`
	ClientIP  string `json:"client_ip,omitempty"`
	SessionID string `json:"session_id,omitempty"`

	HTTPMethod  string            `json:"http_method,omitempty"`
	HTTPPath    string            `json:"http_path,omitempty"`
	QueryParams map[string]string `json:"query_params,omitempty"`
	HTTPStatus  *int              `json:"http_status,omitempty"`
	DurationMs  *float64          `json:"duration_ms,omitempty"`
	BytesIn     *int64            `json:"bytes_in,omitempty"`
	BytesOut    *int64            `json:"bytes_out,omitempty"`

	RequestBody  string `json:"request_body,omitempty"`
	ResponseBody string `json:"response_body,omitempty"`

	Event   string `json:"event,omitempty"`
	Message string `json:"message"`
	Payload string `json:"payload,omitempty"`

	ErrorType    string `json:"error_type,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`
	StackTrace   string `json:"stack_trace,omitempty"`

	ChildWorkflowID  string `json:"child_workflow_id,omitempty"`
	RunID            string `json:"run_id,omitempty"`
	ParentWorkflowID string `json:"parent_workflow_id,omitempty"`

	Integration *IntegrationInfo `json:"integration,omitempty"`
	Queue       *QueueInfo       `json:"queue,omitempty"`
	Job         *JobInfo         `json:"job,omitempty"`

	Extra      map[string]any  `json:"extra,omitempty"`
	Kubernetes *KubernetesInfo `json:"kubernetes,omitempty"`
}

// stringifyJSON renders v as a compact JSON string. Strings are passed through
// unchanged (so already-serialized bodies are not double-encoded).
//
// A marshalling failure is reported rather than swallowed: silently emitting an
// empty payload would drop the very data the caller asked to log.
func stringifyJSON(v any) (string, error) {
	if v == nil {
		return "", nil
	}
	if s, ok := v.(string); ok {
		return s, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// renderFailure is written in place of a value that cannot be serialized, so the
// log line records that data was dropped instead of appearing to have none.
func renderFailure(err error) string {
	return "[unserializable: " + err.Error() + "]"
}

// stringifyOrFailure renders v, falling back to a visible marker when it cannot
// be marshalled. Used where a single bad nested value must not cost the whole
// log line.
func stringifyOrFailure(v any) string {
	s, err := stringifyJSON(v)
	if err != nil {
		return renderFailure(err)
	}
	return s
}

// MarshalJSON renders IntegrationInfo with request_body/response_body as
// stringified JSON (a JSON string whose content is itself JSON).
func (i IntegrationInfo) MarshalJSON() ([]byte, error) {
	type alias struct {
		Target             string            `json:"target,omitempty"`
		Status             IntegrationStatus `json:"status,omitempty"`
		ExternalDurationMs *float64          `json:"external_duration_ms,omitempty"`
		RetryCount         *int              `json:"retry_count,omitempty"`
		RequestBody        *string           `json:"request_body,omitempty"`
		ResponseBody       *string           `json:"response_body,omitempty"`
	}
	a := alias{
		Target:             i.Target,
		Status:             i.Status,
		ExternalDurationMs: i.ExternalDurationMs,
		RetryCount:         i.RetryCount,
	}
	if i.RequestBody != nil {
		s := stringifyOrFailure(i.RequestBody)
		a.RequestBody = &s
	}
	if i.ResponseBody != nil {
		s := stringifyOrFailure(i.ResponseBody)
		a.ResponseBody = &s
	}
	return json.Marshal(a)
}
