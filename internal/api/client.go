package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const BaseURL = "https://cloud-api.nan.builders/api"

type Client struct {
	token string
	http  *http.Client
}

func New(token string) *Client {
	return &Client{token: token, http: &http.Client{}}
}

func (c *Client) get(path string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, BaseURL+path, nil)
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

func (c *Client) GetAgentsModels() (any, error) {
	body, err := c.get("/agents/models")
	if err != nil {
		return nil, err
	}
	var result any
	return result, json.Unmarshal(body, &result)
}
