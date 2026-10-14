package api

import (
	"context"
)

// Secret kinds: a generic secret an automation hands to its runs, or a
// provider key a run or an automation uses for inference (BYOK).
const (
	SecretKindGeneric     = "generic"
	SecretKindProviderKey = "provider_key"
)

// Secret is a member secret's metadata. The platform never returns a value
// (nor its ciphertext): Hint is at most the last characters, as the portal
// shows them, so a member can tell two tokens apart. A provider key (BYOK)
// also has its Provider and, for openai_compatible only, its BaseURL, which
// is configuration, not a credential.
type Secret struct {
	ID          string  `json:"id"`
	Object      string  `json:"object,omitempty"`
	Name        string  `json:"name"`
	Kind        string  `json:"kind"`
	Provider    *string `json:"provider"`
	BaseURL     *string `json:"base_url"`
	Hint        *string `json:"hint"`
	ValueBytes  int     `json:"value_bytes"`
	Description string  `json:"description"`
	UsedBy      []Ref   `json:"used_by"`
	// UpdatedBy is the credential kind of the last write: browser_session,
	// cli_session or platform_token (write API).
	UpdatedBy  string  `json:"updated_by,omitempty"`
	CreatedAt  string  `json:"created_at"`
	UpdatedAt  string  `json:"updated_at"`
	LastUsedAt *string `json:"last_used_at"`
}

type SecretList struct {
	Data []Secret `json:"data"`
}

// ListSecrets is GET /api/account/secrets. The platform answers it for a
// signed-in session only: API keys and platform tokens are refused there.
func (c *RunsClient) ListSecrets(ctx context.Context) (*SecretList, error) {
	list := &SecretList{}
	return list, c.getJSON(ctx, "/api/account/secrets", list)
}
