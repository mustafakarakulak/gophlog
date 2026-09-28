package gophlog

// IntegrationInfo holds metadata about an external integration call.
//
// RequestBody and ResponseBody are rendered as stringified JSON in the final
// log output (a JSON string whose content is itself JSON); a string is written
// unchanged. Struct values in them honour their `mask` tags, as in a payload;
// name-based strategies do not reach them. Problems are not reported to
// WithOnError: a tag naming no strategy hides the field, a cycle through
// map[string]any / []any values or tagged structs is cut with a marker, and
// any other unserializable value is written as "[unserializable: ...]".
type IntegrationInfo struct {
	Target             string            `json:"target,omitempty"`
	Status             IntegrationStatus `json:"status,omitempty"`
	ExternalDurationMs *float64          `json:"external_duration_ms,omitempty"`
	RetryCount         *int              `json:"retry_count,omitempty"`
	RequestBody        any               `json:"request_body,omitempty"`
	ResponseBody       any               `json:"response_body,omitempty"`
}

// QueueInfo holds metadata about a queue message.
type QueueInfo struct {
	QueueName  string `json:"queue_name,omitempty"`
	MessageID  string `json:"message_id,omitempty"`
	RetryCount *int   `json:"retry_count,omitempty"`
	Ack        *bool  `json:"ack,omitempty"`
}

// JobInfo holds metadata about a background job.
type JobInfo struct {
	Name     string `json:"name,omitempty"`
	Schedule string `json:"schedule,omitempty"`
	RunID    string `json:"run_id,omitempty"`
}

// KubernetesInfo holds Kubernetes pod metadata.
type KubernetesInfo struct {
	PodName       string `json:"pod_name,omitempty"`
	Namespace     string `json:"namespace,omitempty"`
	NodeName      string `json:"node_name,omitempty"`
	ContainerName string `json:"container_name,omitempty"`
}
