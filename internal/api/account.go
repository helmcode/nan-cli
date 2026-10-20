package api

import (
	"context"
	"encoding/json"
	"strconv"
)

// Me is what this CLI shows of the signed-in member, and all it reads of
// /api/auth/me. The platform's answer also carries its own workings - feature
// flags, entitlements, roles, the deployment it runs in - which are not the
// member's business and not this CLI's to print, so they are never decoded:
// a field the platform adds tomorrow stays out until somebody chooses it.
type Me struct {
	Email string `json:"email"`
	// Tier is the plan: free, inference and so on.
	Tier string `json:"tier"`
	// GraceUntil is set only while a membership that ended keeps its apps
	// and data, RFC 3339.
	GraceUntil string `json:"graceUntil,omitempty"`
}

// Me fetches the signed-in member.
func (c *Client) Me(ctx context.Context) (*Me, error) {
	var out Me
	if err := c.get(ctx, "/api/auth/me", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Count is a token count off the wire. The platform sends numbers, and a
// field that arrives as something else reads as zero rather than failing
// the whole answer: one odd figure should not blank the Usage area.
type Count float64

// UnmarshalJSON takes a number, a numeric string, or anything else as zero.
func (n *Count) UnmarshalJSON(b []byte) error {
	var f float64
	if err := json.Unmarshal(b, &f); err == nil {
		*n = Count(f)
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			*n = Count(f)
			return nil
		}
	}
	*n = 0
	return nil
}

// ModelUsage is what one model took in a period.
type ModelUsage struct {
	Model        string `json:"model"`
	InputTokens  Count  `json:"inputTokens"`
	OutputTokens Count  `json:"outputTokens"`
}

// Total is input and output together.
func (m ModelUsage) Total() float64 { return float64(m.InputTokens + m.OutputTokens) }

// Period is one window of usage: its total, and the models behind it in the
// order the platform sends them.
type Period struct {
	TotalTokens Count        `json:"totalTokens"`
	ByModel     []ModelUsage `json:"byModel"`
}

// Sum is input and output summed over the models, which is what a price is
// applied to; TotalTokens is the platform's own figure for the headline.
func (p Period) Sum() (in, out float64) {
	for _, m := range p.ByModel {
		in += float64(m.InputTokens)
		out += float64(m.OutputTokens)
	}
	return in, out
}

// Usage is the member's spend over the three windows the platform keeps.
type Usage struct {
	Last24h Period `json:"last24h"`
	Last30d Period `json:"last30d"`
	AllTime Period `json:"allTime"`
}

// Usage fetches the member's usage.
func (c *Client) Usage(ctx context.Context) (*Usage, error) {
	var out Usage
	if err := c.get(ctx, "/api/metrics/usage", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UsageRaw is the same answer untouched, for `nan metrics usage`, which
// prints what the platform sent rather than what this version understood of
// it.
func (c *Client) UsageRaw(ctx context.Context) (map[string]any, error) {
	var out map[string]any
	if err := c.get(ctx, "/api/metrics/usage", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// KeyStatus is what the platform will say about a member's API key. Note
// what is not in it: the key. The secret is handed over once, when it is
// created, and never again, so Setup cannot fetch a key on a member's
// behalf, only tell them whether they have one and where it lives.
type KeyStatus struct {
	Exists bool   `json:"exists"`
	Alias  string `json:"keyAlias"`
	Name   string `json:"keyName"`
	Region string `json:"region"`
	Synced bool   `json:"secretSynced"`
}

// KeyStatus fetches it.
func (c *Client) KeyStatus(ctx context.Context) (*KeyStatus, error) {
	var out KeyStatus
	if err := c.get(ctx, "/api/keys", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AgentModel is one entry of the platform's own model list: a deployment
// name and what it does, which is what Models falls back to when there is
// no API key to ask the inference API with.
type AgentModel struct {
	Name string `json:"name"`
	Mode string `json:"mode"`
}

// AgentModels fetches the platform's list. It answers either `{"models":
// [...]}` or the bare array, and both are read.
func (c *Client) AgentModels(ctx context.Context) ([]AgentModel, error) {
	var raw json.RawMessage
	if err := c.get(ctx, "/api/agents/models", &raw); err != nil {
		return nil, err
	}
	var wrapped struct {
		Models []AgentModel `json:"models"`
	}
	if err := json.Unmarshal(raw, &wrapped); err == nil && wrapped.Models != nil {
		return wrapped.Models, nil
	}
	var bare []AgentModel
	if err := json.Unmarshal(raw, &bare); err == nil {
		return bare, nil
	}
	return nil, nil
}
