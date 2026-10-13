package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
)

// SecretAssignment is a member secret an automation's runs receive, by name:
// never a value. Exposure "agent" puts it in the agent's environment;
// "actions" gives it only to the platform's action executor.
type SecretAssignment struct {
	Secret   string `json:"secret"`
	Env      string `json:"env"`
	Exposure string `json:"exposure"`
}

// Inference is where a run's or an automation's model calls go: NaN's own
// inference ({"source":"nan"}), or the member's provider key
// ({"source":"byok"}, BYOK) with the key's provider, the secret's name and
// the model. The key itself is never part of it.
type Inference struct {
	Source   string  `json:"source"`
	Provider *string `json:"provider,omitempty"`
	Secret   *string `json:"secret,omitempty"`
	Model    *string `json:"model,omitempty"`
}

// Inference sources, as the platform names them.
const (
	InferenceNaN  = "nan"
	InferenceBYOK = "byok"
)

// BYOK reports whether the inference is the member's own provider key.
func (in *Inference) BYOK() bool { return in != nil && in.Source == InferenceBYOK }

// Automation mirrors the automation object of /v1/automations. It is the
// member's own configuration, read here and never written: creating and
// editing automations happens in the portal.
type Automation struct {
	ID                 string             `json:"id"`
	Object             string             `json:"object,omitempty"`
	Name               string             `json:"name"`
	Description        string             `json:"description"`
	Template           string             `json:"template"`
	Workspace          *Ref               `json:"workspace"`
	Agent              string             `json:"agent"`
	Model              *string            `json:"model"`
	Instructions       string             `json:"instructions,omitempty"`
	Cwd                string             `json:"cwd"`
	GitIsolation       *bool              `json:"git_isolation"`
	TimeoutSeconds     int                `json:"timeout_seconds"`
	ApprovalKinds      []string           `json:"approval_kinds"`
	ApprovalTTLSeconds int                `json:"approval_ttl_seconds"`
	Inference          *Inference         `json:"inference,omitempty"`
	GitHubRepo         *string            `json:"github_repo"`
	Secrets            []SecretAssignment `json:"secrets"`
	MaxRunsPerHour     int                `json:"max_runs_per_hour"`
	ConcurrencyPolicy  string             `json:"concurrency_policy"`
	MaxTriggerDepth    int                `json:"max_trigger_depth"`
	Enabled            bool               `json:"enabled"`
	DisabledReason     *string            `json:"disabled_reason"`
	CreatedAt          string             `json:"created_at"`
	UpdatedAt          string             `json:"updated_at"`
}

type AutomationList struct {
	Object string       `json:"object"`
	Data   []Automation `json:"data"`
}

// Trigger is a webhook or a schedule of an automation. The webhook URL and
// its signing secret are shown once, in the portal, and never again: only
// the URL's prefix is here.
type Trigger struct {
	ID              string  `json:"id"`
	Kind            string  `json:"kind"`
	Enabled         bool    `json:"enabled"`
	TokenPrefix     *string `json:"token_prefix,omitempty"`
	SignatureScheme *string `json:"signature_scheme,omitempty"`
	Cron            *string `json:"cron,omitempty"`
	Timezone        *string `json:"timezone,omitempty"`
	NextFireAt      *string `json:"next_fire_at,omitempty"`
	LastFiredAt     *string `json:"last_fired_at,omitempty"`
	LastOutcome     *string `json:"last_outcome,omitempty"`
	PrevSecretUntil *string `json:"prev_secret_until,omitempty"`
	CreatedAt       string  `json:"created_at,omitempty"`
}

type TriggerList struct {
	Data []Trigger `json:"data"`
}

// Delivery is one entry of an automation's webhook delivery log: what came
// in and what became of it. No payload and no headers, by design.
type Delivery struct {
	DeliveryID string  `json:"delivery_id"`
	TriggerID  string  `json:"trigger_id,omitempty"`
	ReceivedAt string  `json:"received_at"`
	Outcome    string  `json:"outcome"`
	RunID      *string `json:"run_id"`
}

type DeliveryList struct {
	Data []Delivery `json:"data"`
}

func (c *RunsClient) getJSON(ctx context.Context, path string, out any) error {
	req, err := c.newRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	_, err = c.do(req, out)
	return err
}

// ListAutomations is GET /v1/automations.
func (c *RunsClient) ListAutomations(ctx context.Context) (*AutomationList, error) {
	list := &AutomationList{}
	return list, c.getJSON(ctx, "/v1/automations", list)
}

// GetAutomation is GET /v1/automations/{id}.
func (c *RunsClient) GetAutomation(ctx context.Context, id string) (*Automation, error) {
	a := &Automation{}
	return a, c.getJSON(ctx, "/v1/automations/"+url.PathEscape(id), a)
}

// ListTriggers is GET /v1/automations/{id}/triggers.
func (c *RunsClient) ListTriggers(ctx context.Context, id string) (*TriggerList, error) {
	list := &TriggerList{}
	return list, c.getJSON(ctx, "/v1/automations/"+url.PathEscape(id)+"/triggers", list)
}

// ListDeliveries is GET /v1/automations/{id}/deliveries.
func (c *RunsClient) ListDeliveries(ctx context.Context, id string, limit int) (*DeliveryList, error) {
	path := "/v1/automations/" + url.PathEscape(id) + "/deliveries"
	if limit > 0 {
		path += "?limit=" + strconv.Itoa(limit)
	}
	list := &DeliveryList{}
	return list, c.getJSON(ctx, path, list)
}

type runAutomationBody struct {
	Input json.RawMessage `json:"input,omitempty"`
}

// RunAutomation starts a run of an automation by hand. input, when given,
// is a JSON object the agent receives as untrusted data next to the
// automation's own instructions, which it never replaces. replayed is true
// when the platform answered an Idempotency-Key it had already seen.
func (c *RunsClient) RunAutomation(ctx context.Context, id string, input json.RawMessage, idempotencyKey string) (run *Run, replayed bool, err error) {
	req, err := c.newRequest(ctx, http.MethodPost, "/v1/automations/"+url.PathEscape(id)+"/runs", runAutomationBody{Input: input})
	if err != nil {
		return nil, false, err
	}
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	run = &Run{}
	status, err := c.do(req, run)
	if err != nil {
		return nil, false, err
	}
	return run, status == http.StatusOK, nil
}
