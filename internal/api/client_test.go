package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The platform answers /keys with metadata and no secret in it: a key is
// handed over once, when it is created, and never again. That is the whole
// reason the Setup tab asks a member to paste theirs instead of fetching it,
// and this pins the shape so the assumption is checked rather than remembered.
func TestKeyStatusIsMetadataAndCarriesNoKey(t *testing.T) {
	var gotPath, gotCookie string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotCookie = r.Header.Get("Cookie")
		_, _ = w.Write([]byte(`{"exists":true,"keyAlias":"an-alias","keyName":"sk-name","region":"EU","secretSynced":true}`))
	}))
	defer srv.Close()

	c := New("session-token", WithBaseURL(srv.URL))
	status, err := c.KeyStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/keys" {
		t.Errorf("asked for %q, want /api/keys", gotPath)
	}
	if !strings.Contains(gotCookie, "nan_session=session-token") {
		t.Errorf("cookie %q does not carry the session", gotCookie)
	}
	if !status.Exists || status.Alias != "an-alias" || status.Region != "EU" || !status.Synced {
		t.Errorf("status parsed wrong: %+v", status)
	}
}

// An account with no key at all is the case the Setup tab has to say
// something useful about, so it must survive the parse rather than read as
// "yes" by accident.
func TestKeyStatusSaysWhenThereIsNoKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"exists":false}`))
	}))
	defer srv.Close()

	c := New("t", WithBaseURL(srv.URL))
	status, err := c.KeyStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Exists {
		t.Error("an account with no key reads as having one")
	}
}

// /api/auth/me also carries the platform's own workings: feature flags,
// entitlements, roles, the deployment it runs in. None of it is the member's
// to see, and `nan me` prints whatever Me holds, so Me holds only what was
// chosen and the rest is never decoded.
func TestMeReadsOnlyWhatIsShown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"email":"a@b.co","tier":"inference","graceUntil":null,` +
			`"userUUID":"u-1","discordId":"d-1","namespace":"ns-1","isAdmin":true,"roles":["x"],` +
			`"environment":"prod","inferenceProfile":"litellm-prod","workspaces_beta":true,` +
			`"features":{"ephemeral":true},"entitlements":{"apps":true},"profile":{"fullName":"A"}}`))
	}))
	defer srv.Close()

	me, err := New("t", WithBaseURL(srv.URL)).Me(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if me.Email != "a@b.co" || me.Tier != "inference" || me.GraceUntil != "" {
		t.Errorf("me parsed wrong: %+v", me)
	}
	out, err := json.Marshal(me)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(out), `{"email":"a@b.co","tier":"inference"}`; got != want {
		t.Errorf("nan me would print %s, want %s", got, want)
	}
}

// The platform answers a session it no longer honours with 401 and
// `invalid session`, and the panel has to tell that apart from a platform that
// is down: one means sign in again, the other means try later.
func TestAnExpiredSessionIsNamedAsOne(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid session"}`))
	}))
	defer srv.Close()

	c := New("stale", WithBaseURL(srv.URL))
	if _, err := c.Me(context.Background()); !errors.Is(err, ErrSessionExpired) {
		t.Errorf("a refused session comes back as %v", err)
	}

	// With no token there was no session to expire, and saying so would send
	// someone who never signed in to sign in "again".
	c.token = ""
	if _, err := c.Me(context.Background()); errors.Is(err, ErrSessionExpired) {
		t.Error("a request with no session reads as an expired one")
	}

	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})
	c.token = "stale"
	if _, err := c.Me(context.Background()); errors.Is(err, ErrSessionExpired) {
		t.Error("a platform that is down reads as an expired session")
	}
}
