package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// InferenceBaseURL is the API a member points their tools at, and the only
// place that knows which ids their key can actually name in a request.
const InferenceBaseURL = "https://api.nan.builders/v1"

// InferenceURL is InferenceBaseURL, or the override for a local stand-in.
func InferenceURL() string {
	return resolveEndpoint(envInferenceURL, InferenceBaseURL)
}

// ErrKeyRefused is the inference API turning a key down.
var ErrKeyRefused = errors.New("the API key in Setup is not valid")

// ListModels returns the ids from GET /v1/models, which is the list the docs
// call definitive: the platform's /agents/models answers with deployment
// names instead, so it carries routing aliases (`-fallback`) and models that
// are on their way out. It is also how a pasted key is checked: an answer
// here is the cluster accepting it.
func ListModels(ctx context.Context, apiKey string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, InferenceURL()+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent())

	resp, err := (&http.Client{Timeout: requestTimeout, CheckRedirect: noRedirects}).Do(req)
	if err != nil {
		return nil, unreachable(err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, ErrKeyRefused
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var parsed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("the inference API sent something this version cannot read: %w", err)
	}
	ids := make([]string, 0, len(parsed.Data))
	for _, m := range parsed.Data {
		ids = append(ids, m.ID)
	}
	return ids, nil
}
