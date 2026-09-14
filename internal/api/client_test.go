package api

import (
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
