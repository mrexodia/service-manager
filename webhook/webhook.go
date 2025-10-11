package webhook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// FailurePayload represents the webhook payload for service failures
type FailurePayload struct {
	ServiceName       string    `json:"service_name"`
	Timestamp         time.Time `json:"timestamp"`
	FailureCount      int       `json:"failure_count"`
	LastExitCode      int       `json:"last_exit_code"`
	ErrorMessage      string    `json:"error_message,omitempty"`
	ConsecutiveErrors int       `json:"consecutive_errors"`
}

// Notifier handles webhook notifications
type Notifier struct {
	webhookURL string
	enabled    bool
	client     *http.Client
}

// NewNotifier creates a new webhook notifier
func NewNotifier(webhookURL string) *Notifier {
	return &Notifier{
		webhookURL: webhookURL,
		enabled:    webhookURL != "",
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// NotifyFailure sends a failure notification to the webhook
func (n *Notifier) NotifyFailure(payload FailurePayload) error {
	if !n.enabled {
		return nil // Webhook disabled
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	req, err := http.NewRequest("POST", n.webhookURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "service-manager/1.0")

	resp, err := n.client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send webhook: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned non-2xx status: %d", resp.StatusCode)
	}

	return nil
}
