package api

import (
	"context"
)

// Secret is a member secret's metadata. The platform never returns a value
// (nor its ciphertext): Hint is at most the last characters, as the portal
// shows them, so a member can tell two tokens apart.
type Secret struct {
	ID          string  `json:"id"`
	Object      string  `json:"object,omitempty"`
	Name        string  `json:"name"`
	Kind        string  `json:"kind"`
	Provider    *string `json:"provider"`
	Hint        *string `json:"hint"`
	ValueBytes  int     `json:"value_bytes"`
	Description string  `json:"description"`
	UsedBy      []Ref   `json:"used_by"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
	LastUsedAt  *string `json:"last_used_at"`
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
