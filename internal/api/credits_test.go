package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The credits answer also carries rates, catalogue text and badges; only
// what the dashboard shows is read.
func TestCreditsReadsOnlyWhatIsShown(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = w.Write([]byte(`{"enabled":true,"tier":"x","periodEnd":"2026-12-01T00:00:00Z","allowanceCredits":500,` +
			`"usedCredits":12.5,"remainingCredits":487.5,"packCredits":0,"packExpiresAt":null,"frozen":false,` +
			`"frontierPaused":false,"cacheSavedCredits":3,"models":[{"model":"m","displayName":"M","description":"d",` +
			`"approxCreditsPerMTokens":1,"rates":{"inputPerM":1},"creditsThisMonth":12.5,"tokensThisMonth":100,"tags":["t"]}]}`))
	}))
	defer srv.Close()

	c, err := New("t", WithBaseURL(srv.URL)).Credits(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if path != "/api/usage/credits" || !c.Enabled || c.UsedCredits != 12.5 || len(c.Models) != 1 || c.Models[0].CreditsThisMonth != 12.5 {
		t.Fatalf("read %s as %+v", path, c)
	}
	out, _ := json.Marshal(c)
	var top map[string]any
	if err := json.Unmarshal(out, &top); err != nil {
		t.Fatal(err)
	}
	if _, ok := top["tier"]; ok {
		t.Errorf("kept the tier: %s", out)
	}
	for _, gone := range []string{`"rates"`, `"description"`, `"tags"`, `"approxCreditsPerMTokens"`} {
		if strings.Contains(string(out), gone) {
			t.Errorf("kept %s: %s", gone, out)
		}
	}
}

func TestDiscordReadsTheLinkAndTheName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/account/discord" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"linked":true,"discordUsername":"nan_member","guildId":"1"}`))
	}))
	defer srv.Close()

	d, err := New("t", WithBaseURL(srv.URL)).Discord(context.Background())
	if err != nil || !d.Linked || d.Username != "nan_member" {
		t.Fatalf("%+v, %v", d, err)
	}
}
