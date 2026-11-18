package api

import (
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

	c := &Client{token: "session-token", http: srv.Client(), baseURL: srv.URL}
	status, err := c.GetKeyStatus()
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/keys" {
		t.Errorf("asked for %q, want /keys", gotPath)
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"exists":false}`))
	}))
	defer srv.Close()

	c := &Client{token: "t", http: srv.Client(), baseURL: srv.URL}
	status, err := c.GetKeyStatus()
	if err != nil {
		t.Fatal(err)
	}
	if status.Exists {
		t.Error("an account with no key reads as having one")
	}
}

// The platform answers a session it no longer honours with 401 and
// `invalid session`, and the panel has to tell that apart from a platform that
// is down: one means sign in again, the other means try later.
func TestAnExpiredSessionIsNamedAsOne(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid session"}`))
	}))
	defer srv.Close()

	c := &Client{token: "stale", http: srv.Client(), baseURL: srv.URL}
	if _, err := c.GetMe(); !errors.Is(err, ErrSessionExpired) {
		t.Errorf("a refused session comes back as %v", err)
	}

	// With no token there was no session to expire, and saying so would send
	// someone who never signed in to sign in "again".
	c.token = ""
	if _, err := c.GetMe(); errors.Is(err, ErrSessionExpired) {
		t.Error("a request with no session reads as an expired one")
	}

	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})
	c.token = "stale"
	if _, err := c.GetMe(); errors.Is(err, ErrSessionExpired) {
		t.Error("a platform that is down reads as an expired session")
	}
}

func TestGetAgentsModels(t *testing.T) {
	var gotURI, gotCookie string
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURI = r.URL.RequestURI()
		gotCookie = r.Header.Get("Cookie")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"models":[
			{"name":"glm5.3-flash","mode":"chat","context_window":1048576,"max_output_tokens":32768,"supports_vision":true,"supports_reasoning":false},
			{"name":"bare","mode":"chat"}]}`))
	}))
	defer srv.Close()
	c := &Client{token: "tok", http: srv.Client(), baseURL: srv.URL}

	got, err := c.GetAgentsModels(false)
	if err != nil {
		t.Fatal(err)
	}
	if gotURI != "/agents/models" {
		t.Errorf("asked %q, want no ?all", gotURI)
	}
	if !strings.Contains(gotCookie, "nan_session=tok") {
		t.Errorf("cookie %q does not carry the session", gotCookie)
	}
	if len(got) != 2 {
		t.Fatalf("got %d models", len(got))
	}
	full := got[0]
	if full.ContextWindow == nil || *full.ContextWindow != 1048576 || *full.MaxOutputTokens != 32768 ||
		full.SupportsVision == nil || !*full.SupportsVision || full.SupportsReasoning == nil || *full.SupportsReasoning {
		t.Errorf("metadata decoded wrong: %+v", full)
	}
	bare := got[1]
	if bare.ContextWindow != nil || bare.MaxOutputTokens != nil || bare.SupportsVision != nil || bare.SupportsReasoning != nil {
		t.Errorf("a missing field did not stay nil: %+v", bare)
	}

	if _, err := c.GetAgentsModels(true); err != nil {
		t.Fatal(err)
	}
	if gotURI != "/agents/models?all=true" {
		t.Errorf("asked %q, want ?all=true", gotURI)
	}

	status = http.StatusUnauthorized
	if _, err := c.GetAgentsModels(false); !errors.Is(err, ErrSessionExpired) {
		t.Errorf("401 with a token gave %v, want ErrSessionExpired", err)
	}
}
