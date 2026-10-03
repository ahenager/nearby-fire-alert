// Package openclaw implements the OpenClaw Gateway HTTP hook client. It posts
// a compact JSON "fire event" payload to a configured hook URL and follows the
// admission semantics:
//
//   - Any HTTP 2xx is treated as ADMISSION (accepted), not delivery of a
//     specific outcome. The client does not inspect `ok`/`enriched`/
//     `deduplicated` booleans in the body; durable "only once" behavior is the
//     service store's responsibility.
//   - The `Idempotency-Key` header is sent as a best-effort supplementary
//     safety net. Note: the Gateway's replay cache is in-memory and expires
//     after ~5 minutes (and clears on Gateway restart), so it is NOT the source
//     of truth for dedup.
//   - 401/429/5xx are retried with exponential backoff, reusing the same
//     Idempotency-Key; other statuses are not retried.
package openclaw

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ahenager/nearby-fire-alert/internal/config"
	"github.com/ahenager/nearby-fire-alert/internal/httpx"
	"github.com/ahenager/nearby-fire-alert/internal/logx"
	"github.com/ahenager/nearby-fire-alert/internal/providerapi"
)

// Payload is the wire format posted to the OpenClaw hook.
type Payload struct {
	Event   EventPayload `json:"event"`
	Summary string       `json:"summary"`
	// Routing is included only when posting directly to /hooks/agent.
	Channel   string `json:"channel,omitempty"`
	To        string `json:"to,omitempty"`
	AccountID string `json:"account_id,omitempty"`
}

// EventPayload mirrors the provider-agnostic event on the wire.
type EventPayload struct {
	EventID         string   `json:"event_id"`
	Kind            string   `json:"kind"`
	Source          string   `json:"source"`
	DetectedAt      string   `json:"detected_at"`
	Latitude        float64  `json:"latitude"`
	Longitude       float64  `json:"longitude"`
	DistanceMiles   float64  `json:"distance_miles"`
	Confidence      float64  `json:"confidence"`
	FRP             *float64 `json:"frp"`
	BrightnessTempK *float64 `json:"brightness_temp_k"`
	IncidentName    *string  `json:"incident_name"`
}

// Result reports the outcome of a delivery attempt. Delivered is true when the
// Gateway ACKNOWLEDGED the post (any 2xx), which the service treats as
// admission; it does not imply the end-to-end message was read by a human.
type Result struct {
	Delivered  bool
	RunID      string
	StatusCode int
}

// Client posts fire events to an OpenClaw Gateway hook.
type Client struct {
	cfg    config.OpenClawConfig
	client *httpx.Client
	logger logx.Logger
}

// New creates a client from the resolved OpenClaw config.
func New(cfg config.OpenClawConfig, logger logx.Logger) *Client {
	timeout := cfg.TimeoutSeconds
	if timeout <= 0 {
		timeout = 15
	}
	return &Client{
		cfg:    cfg,
		client: httpx.NewClient(time.Duration(timeout) * time.Second),
		logger: logger,
	}
}

// BuildPayload constructs the wire payload for an event.
func BuildPayload(ev providerapi.Event, summary string, cfg config.OpenClawConfig) Payload {
	p := Payload{
		Event: EventPayload{
			EventID:       ev.EventID,
			Kind:          ev.Kind,
			Source:        ev.Source,
			DetectedAt:    ev.DetectedAt.UTC().Format(time.RFC3339Nano),
			Latitude:      ev.Latitude,
			Longitude:     ev.Longitude,
			DistanceMiles: ev.DistanceMiles,
			Confidence:    ev.Confidence,
		},
		Summary: summary,
	}
	if ev.FRP > 0 {
		v := ev.FRP
		p.Event.FRP = &v
	}
	if ev.BrightnessTempK > 0 {
		v := ev.BrightnessTempK
		p.Event.BrightnessTempK = &v
	}
	if ev.IncidentName != "" {
		v := ev.IncidentName
		p.Event.IncidentName = &v
	}
	// Include delivery routing when posting directly to the agent hook.
	if isAgentHook(cfg.HookURL) {
		p.Channel = cfg.Channel
		p.To = cfg.To
		p.AccountID = cfg.AccountID
	}
	return p
}

func isAgentHook(url string) bool {
	return strings.Contains(url, "/hooks/agent")
}

// Summary builds a one-sentence, human-readable alert summary.
func Summary(ev providerapi.Event) string {
	kind := "hotspot"
	if ev.Kind == "perimeter" {
		kind = "perimeter"
	}
	name := ev.IncidentName
	if name == "" {
		name = "unnamed fire"
	}
	return fmt.Sprintf(
		"ALERT: %s fire %s detected %.1f mi from home (%s, conf %.0f%s) at %.4f, %.4f.",
		kind, name, ev.DistanceMiles, ev.Source, ev.Confidence,
		frpSuffix(ev.FRP), ev.Latitude, ev.Longitude,
	)
}

func frpSuffix(frp float64) string {
	if frp > 0 {
		return fmt.Sprintf(", FRP %.0f", frp)
	}
	return ""
}

// Post delivers the event. On any 2xx the Gateway has ADMITTED the request;
// the service logs the runId and does not retry. The body's ok/enriched/
// deduplicated fields are intentionally ignored (we rely on our own store for
// durable dedup). 401/429/5xx are retried with exponential backoff using the
// same Idempotency-Key; a non-2xx after retries returns an error.
func (c *Client) Post(ctx context.Context, payload Payload) (Result, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return Result{}, fmt.Errorf("marshal payload: %w", err)
	}

	var lastStatus int
	runID := ""
	admitted := false

	err = httpx.Retry(ctx, httpx.RetryOption{
		MaxAttempts: 4,
		InitialWait: 500 * time.Millisecond,
		MaxWait:     10 * time.Second,
		ShouldRetry: func(code int) bool {
			return code == http.StatusUnauthorized ||
				code == http.StatusTooManyRequests ||
				code >= 500
		},
	}, func() (bool, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.HookURL, bytes.NewReader(body))
		if err != nil {
			return false, err
		}
		req.Header.Set("Content-Type", "application/json")
		if c.cfg.HookToken != "" {
			req.Header.Set("Authorization", "Bearer "+c.cfg.HookToken)
		}
		req.Header.Set("Idempotency-Key", payload.Event.EventID)

		resp, err := c.client.HTTP.Do(req)
		if err != nil {
			return false, err
		}
		defer resp.Body.Close()
		respBody, _ := io.ReadAll(resp.Body)
		lastStatus = resp.StatusCode

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			admitted = true
			runID = extractRunID(respBody)
			return true, nil
		}
		return false, fmt.Errorf("openclaw: HTTP %d: %s", resp.StatusCode, truncate(respBody))
	})

	if err != nil {
		return Result{StatusCode: lastStatus}, err
	}
	return Result{Delivered: admitted, RunID: runID, StatusCode: lastStatus}, nil
}

func extractRunID(body []byte) string {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return ""
	}
	// Gateway returns "runId"; be tolerant of a few common variants.
	for _, k := range []string{"runId", "run_id", "RunID", "requestId"} {
		if v, ok := m[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

func truncate(b []byte) string {
	if len(b) > 200 {
		return string(b[:200])
	}
	return string(b)
}
