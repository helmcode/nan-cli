package api

import "context"

// Credits is what the dashboard shows of a member's credits this billing
// period, and all it reads of /api/usage/credits: the pool, what is left of
// it, the pack bought on top and when it expires, and per model what was
// spent. The rest of the answer (rates, catalogue text, badges) is never
// decoded. A member not on credits gets Enabled false and nothing else.
type Credits struct {
	Enabled bool `json:"enabled"`
	// PeriodEnd is when the pool refills, RFC 3339.
	PeriodEnd        string  `json:"periodEnd"`
	AllowanceCredits float64 `json:"allowanceCredits"`
	UsedCredits      float64 `json:"usedCredits"`
	RemainingCredits float64 `json:"remainingCredits"`
	// PackCredits are bought credits, spent after the pool; PackExpiresAt
	// is when the unspent ones lapse.
	PackCredits   float64 `json:"packCredits"`
	PackExpiresAt *string `json:"packExpiresAt"`
	// Frozen is a member on credits that cannot spend them right now (a
	// membership that ended, a failed payment): every metered request is
	// refused.
	Frozen bool `json:"frozen"`
	// FrontierPaused is the platform holding back the frontier models for
	// everyone for a while; the rest still answer.
	FrontierPaused bool           `json:"frontierPaused"`
	Models         []ModelCredits `json:"models"`
	// CacheSavedCredits is what the prompt cache saved this period, as the
	// web dashboard says it.
	CacheSavedCredits float64 `json:"cacheSavedCredits"`
}

// ModelCredits is what one model cost this period.
type ModelCredits struct {
	Model            string  `json:"model"`
	DisplayName      string  `json:"displayName"`
	CreditsThisMonth float64 `json:"creditsThisMonth"`
	TokensThisMonth  Count   `json:"tokensThisMonth"`
}

// Credits fetches the member's credits for this billing period.
func (c *Client) Credits(ctx context.Context) (*Credits, error) {
	var out Credits
	if err := c.get(ctx, "/api/usage/credits", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Discord is whether the account is linked to Discord, and as whom. The
// numeric Discord id the platform also keeps is never read.
type Discord struct {
	Linked   bool   `json:"linked"`
	Username string `json:"discordUsername"`
}

// Discord fetches the account's Discord link.
func (c *Client) Discord(ctx context.Context) (*Discord, error) {
	var out Discord
	if err := c.get(ctx, "/api/account/discord", &out); err != nil {
		return nil, err
	}
	return &out, nil
}
