package logging

// IntegrationInfo holds metadata about an external integration call.
//
// RequestBody and ResponseBody are rendered as stringified JSON in the final
// log output (matching the .NET PayloadConverter behaviour).
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
