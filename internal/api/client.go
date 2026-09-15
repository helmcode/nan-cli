package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const BaseURL = "https://cloud-api.nan.builders/api"

// A client with no timeout waits forever on a connection that is accepted and
// then never answered, which in the panel is a spinner that never stops.
const requestTimeout = 30 * time.Second

type Client struct {
	token string
	http  *http.Client
	// Defaults to BaseURL. A field rather than the constant so the tests can
	// point the client at a server of their own.
	baseURL string
}

func New(token string) *Client {
	return &Client{token: token, http: &http.Client{Timeout: requestTimeout}, baseURL: BaseURL}
}

// Token is what this client sends. The panel builds a client once, at start,
// and has to build another when a member signs in from inside it - a client
// still holding the token it started with sends an empty cookie and the
// platform answers `unauthorized`, which reads exactly like a failed login.
func (c *Client) Token() string { return c.token }

func (c *Client) get(path string) ([]byte, error) {
	base := c.baseURL
	if base == "" {
		base = BaseURL
	}
	req, err := http.NewRequest(http.MethodGet, base+path, nil)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Cookie", "nan_session="+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &e) == nil && e.Error != "" {
			return nil, fmt.Errorf("%s", e.Error)
		}
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return body, nil
}

func (c *Client) GetMe() (map[string]any, error) {
	body, err := c.get("/auth/me")
	if err != nil {
		return nil, err
	}
	var result map[string]any
	return result, json.Unmarshal(body, &result)
}

func (c *Client) GetMetricsUsage() (map[string]any, error) {
	body, err := c.get("/metrics/usage")
	if err != nil {
		return nil, err
	}
	var result map[string]any
	return result, json.Unmarshal(body, &result)
}

// InferenceBaseURL is the API a member points their tools at, and the only
// place that knows which ids their key can actually name in a request.
const InferenceBaseURL = "https://api.nan.builders/v1"

// ListModels returns the ids from GET /v1/models, which is the list the docs
// call definitive: `/agents/models` on the platform answers with deployment
// names instead, so it carries routing aliases (`-fallback`) and models that
// are on their way out.
func ListModels(apiKey string) ([]string, error) {
	req, err := http.NewRequest(http.MethodGet, InferenceBaseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := (&http.Client{Timeout: requestTimeout}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("the API key in Setup is not valid")
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var parsed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(parsed.Data))
	for _, m := range parsed.Data {
		ids = append(ids, m.ID)
	}
	return ids, nil
}

// KeyStatus is what the platform will say about a member's API key. Note what
// is not in it: the key. GET /api/keys answers with metadata only - the secret
// is handed over once, when it is created, and never again - so the Setup tab
// cannot fetch a key on a member's behalf, only tell them whether they have
// one and where it lives.
type KeyStatus struct {
	Exists bool   `json:"exists"`
	Alias  string `json:"keyAlias"`
	Name   string `json:"keyName"`
	Region string `json:"region"`
	Synced bool   `json:"secretSynced"`
}

func (c *Client) GetKeyStatus() (*KeyStatus, error) {
	body, err := c.get("/keys")
	if err != nil {
		return nil, err
	}
	var status KeyStatus
	if err := json.Unmarshal(body, &status); err != nil {
		return nil, err
	}
	return &status, nil
}

func (c *Client) GetAgentsModels() (any, error) {
	body, err := c.get("/agents/models")
	if err != nil {
		return nil, err
	}
	var result any
	return result, json.Unmarshal(body, &result)
}
