package api

import "context"

// Ephemeral runs: an agent in a fresh, throwaway container instead of a
// workspace, billed per started minute by container size. The member's
// limits and this month's metering live under /api/ephemeral, which takes a
// session only (the CLI's or the browser's) and answers 404 to a member who
// does not have ephemeral runs. Writing the limits needs the browser.

// Container sizes, as the platform names them.
const (
	SizeS = "s"
	SizeM = "m"
	SizeL = "l"
)

// EphemeralUsage is GET /api/ephemeral/usage: this calendar month (UTC).
// Money is in EUR, and also in micros (millionths of a euro) to stay exact.
type EphemeralUsage struct {
	Month         string  `json:"month"`
	Seconds       int64   `json:"seconds"`
	Minutes       int64   `json:"minutes"`
	CostEUR       float64 `json:"cost_eur"`
	CostEURMicros int64   `json:"cost_eur_micros"` // chargeable: after the free minutes
	Plan          string  `json:"plan"`

	// The free allowance, priced in S-minutes: one M minute uses two, one L
	// minute four.
	FreeEURMicrosTotal int64 `json:"free_eur_micros_total"`
	FreeEURMicrosUsed  int64 `json:"free_eur_micros_used"`
	FreeEURMicrosLeft  int64 `json:"free_eur_micros_left"`
	FreeMinutesLeft    int64 `json:"free_minutes_left"` // S-minutes

	MonthlyCapEURCents       int              `json:"monthly_cap_eur_cents"`
	PricesEURMicrosPerMinute map[string]int64 `json:"prices_eur_micros_per_minute"`
	// BillingEnabled false: usage is metered and shown, not charged.
	BillingEnabled bool `json:"billing_enabled"`
}

// EphemeralLimits are the ranges the platform accepts for the settings.
type EphemeralLimits struct {
	MonthlyCapEURCentsMax int `json:"monthly_cap_eur_cents_max"`
	MaxConcurrentMax      int `json:"max_concurrent_max"`
}

// EphemeralSettings is GET /api/ephemeral/settings: the member's own
// limits. They are changed in the portal only.
type EphemeralSettings struct {
	MonthlyCapEURCents int             `json:"monthly_cap_eur_cents"`
	MonthlyCapEUR      float64         `json:"monthly_cap_eur"`
	MaxConcurrent      int             `json:"max_concurrent"`
	Limits             EphemeralLimits `json:"limits"`
	BillingEnabled     bool            `json:"billing_enabled"`
}

// EphemeralUsage is GET /api/ephemeral/usage (a session only).
func (c *RunsClient) EphemeralUsage(ctx context.Context) (*EphemeralUsage, error) {
	u := &EphemeralUsage{}
	return u, c.getJSON(ctx, "/api/ephemeral/usage", u)
}

// EphemeralSettings is GET /api/ephemeral/settings (a session only).
func (c *RunsClient) EphemeralSettings(ctx context.Context) (*EphemeralSettings, error) {
	s := &EphemeralSettings{}
	return s, c.getJSON(ctx, "/api/ephemeral/settings", s)
}
