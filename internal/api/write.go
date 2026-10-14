package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
)

// The write API (AGENT-AUTOMATIONS-PHASE3-WRITE-API.md): secrets and
// automations written from a CLI session or a platform token with the
// secrets:write / automations:write scope. The server decides whether a
// credential may write; this client only sends the request and reads the
// answer. Secret values go out in request bodies and never come back.

// Features is the part of GET /api/auth/me this CLI reads.
type Features struct {
	WriteAPI    bool `json:"write_api"`
	Secrets     bool `json:"secrets"`
	Automations bool `json:"automations"`
}

// Features is GET /api/auth/me, read for its feature flags.
func (c *RunsClient) Features(ctx context.Context) (*Features, error) {
	var me struct {
		Features Features `json:"features"`
	}
	if err := c.getJSON(ctx, "/api/auth/me", &me); err != nil {
		return nil, err
	}
	return &me.Features, nil
}

// SecretsV1 is GET /v1/secrets: metadata only, for a session or a token
// with secrets:read.
func (c *RunsClient) SecretsV1(ctx context.Context) (*SecretList, error) {
	list := &SecretList{}
	return list, c.getJSON(ctx, "/v1/secrets", list)
}

// CreateSecretRequest is the body of POST /v1/secrets. Its value is never
// printed: String and GoString redact it, so a %v or %#v in an error or a
// debug line cannot leak it.
type CreateSecretRequest struct {
	Name        string  `json:"name"`
	Value       string  `json:"value"`
	Kind        string  `json:"kind,omitempty"`
	Provider    string  `json:"provider,omitempty"`
	BaseURL     string  `json:"base_url,omitempty"`
	Description *string `json:"description,omitempty"`
}

func (r CreateSecretRequest) String() string {
	return "CreateSecretRequest{name:" + r.Name + " value:[redacted]}"
}
func (r CreateSecretRequest) GoString() string { return r.String() }

// CreateSecret is POST /v1/secrets. idempotencyKey, when set, makes a retry
// answer the secret the first attempt created (replayed is then true).
func (c *RunsClient) CreateSecret(ctx context.Context, in CreateSecretRequest, idempotencyKey string) (s *Secret, replayed bool, err error) {
	req, err := c.newRequest(ctx, http.MethodPost, "/v1/secrets", in)
	if err != nil {
		return nil, false, err
	}
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	s = &Secret{}
	status, err := c.do(req, s)
	if err != nil {
		return nil, false, err
	}
	return s, status == http.StatusOK, nil
}

type secretValueBody struct {
	Value string `json:"value"`
}

func (secretValueBody) String() string { return "secretValueBody{[redacted]}" }

// ReplaceSecretValue is PUT /v1/secrets/{id}/value.
func (c *RunsClient) ReplaceSecretValue(ctx context.Context, id, value string) (*Secret, error) {
	s := &Secret{}
	return s, c.sendJSON(ctx, http.MethodPut, "/v1/secrets/"+url.PathEscape(id)+"/value", secretValueBody{Value: value}, s)
}

// SecretPatch is the body of PATCH /v1/secrets/{id}: nil fields are left
// as they are.
type SecretPatch struct {
	Description *string `json:"description,omitempty"`
	BaseURL     *string `json:"base_url,omitempty"`
}

// UpdateSecret is PATCH /v1/secrets/{id}.
func (c *RunsClient) UpdateSecret(ctx context.Context, id string, p SecretPatch) (*Secret, error) {
	s := &Secret{}
	return s, c.sendJSON(ctx, http.MethodPatch, "/v1/secrets/"+url.PathEscape(id), p, s)
}

// DeleteSecret is DELETE /v1/secrets/{id}. A secret an automation uses is
// a 409 secret_in_use whose APIError names the automations.
func (c *RunsClient) DeleteSecret(ctx context.Context, id string) error {
	return c.sendJSON(ctx, http.MethodDelete, "/v1/secrets/"+url.PathEscape(id), nil, nil)
}

// CreateAutomation is POST /v1/automations with the member's own body,
// passed through as written: the server validates every field.
func (c *RunsClient) CreateAutomation(ctx context.Context, body json.RawMessage) (*Automation, json.RawMessage, error) {
	return c.automationWrite(ctx, http.MethodPost, "/v1/automations", body)
}

// UpdateAutomation is PATCH /v1/automations/{id}: only the fields in body
// change.
func (c *RunsClient) UpdateAutomation(ctx context.Context, id string, body json.RawMessage) (*Automation, json.RawMessage, error) {
	return c.automationWrite(ctx, http.MethodPatch, "/v1/automations/"+url.PathEscape(id), body)
}

func (c *RunsClient) automationWrite(ctx context.Context, method, path string, body json.RawMessage) (*Automation, json.RawMessage, error) {
	var raw json.RawMessage
	if err := c.sendJSON(ctx, method, path, body, &raw); err != nil {
		return nil, nil, err
	}
	a := &Automation{}
	if err := json.Unmarshal(raw, a); err != nil {
		return nil, nil, err
	}
	return a, raw, nil
}

// DeleteAutomation is DELETE /v1/automations/{id}.
func (c *RunsClient) DeleteAutomation(ctx context.Context, id string) error {
	return c.sendJSON(ctx, http.MethodDelete, "/v1/automations/"+url.PathEscape(id), nil, nil)
}

// TriggerSecrets is what creating or rotating a trigger answers once and
// never again: the webhook URL (it carries the token that authenticates a
// delivery) and the signing secret. Absent fields were not issued.
type TriggerSecrets struct {
	Trigger       *Trigger `json:"trigger,omitempty"`
	WebhookURL    *string  `json:"webhook_url,omitempty"`
	SigningSecret *string  `json:"signing_secret,omitempty"`
}

// CreateTrigger is POST /v1/automations/{id}/triggers with the member's own
// body. Not idempotent: after a lost answer, list the triggers and rotate or
// delete the one you cannot account for.
func (c *RunsClient) CreateTrigger(ctx context.Context, automationID string, body json.RawMessage) (*TriggerSecrets, error) {
	out := &TriggerSecrets{}
	return out, c.sendJSON(ctx, http.MethodPost, "/v1/automations/"+url.PathEscape(automationID)+"/triggers", body, out)
}

// UpdateTrigger is PATCH /v1/automations/{id}/triggers/{tid}.
func (c *RunsClient) UpdateTrigger(ctx context.Context, automationID, triggerID string, body json.RawMessage) (*Trigger, error) {
	t := &Trigger{}
	return t, c.sendJSON(ctx, http.MethodPatch, triggerPath(automationID, triggerID), body, t)
}

// RotateRequest is the body of POST .../rotate: what to rotate ("url",
// "secret") and whether the previous signing secret stays valid for 24 h.
type RotateRequest struct {
	Rotate []string `json:"rotate"`
	Grace  bool     `json:"grace"`
}

// RotateTrigger is POST /v1/automations/{id}/triggers/{tid}/rotate.
func (c *RunsClient) RotateTrigger(ctx context.Context, automationID, triggerID string, in RotateRequest) (*TriggerSecrets, error) {
	out := &TriggerSecrets{}
	return out, c.sendJSON(ctx, http.MethodPost, triggerPath(automationID, triggerID)+"/rotate", in, out)
}

// DeleteTrigger is DELETE /v1/automations/{id}/triggers/{tid}.
func (c *RunsClient) DeleteTrigger(ctx context.Context, automationID, triggerID string) error {
	return c.sendJSON(ctx, http.MethodDelete, triggerPath(automationID, triggerID), nil, nil)
}

func triggerPath(automationID, triggerID string) string {
	return "/v1/automations/" + url.PathEscape(automationID) + "/triggers/" + url.PathEscape(triggerID)
}

// sendJSON sends body (nil for none; a json.RawMessage goes as it is) and
// decodes a 2xx answer into out (nil to ignore it, as for a 204).
func (c *RunsClient) sendJSON(ctx context.Context, method, path string, body, out any) error {
	req, err := c.newRequest(ctx, method, path, body)
	if err != nil {
		return err
	}
	_, err = c.do(req, out)
	return err
}
