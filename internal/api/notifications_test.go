package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The notification channel objects as cloud-api serialises them
// (helmcode/nan-cloud-api#349, internal/handlers/notification_channels.go):
// testdata/phase3/notification_*.json.
func TestGoldenNotificationChannels(t *testing.T) {
	var list NotificationChannelList
	golden3(t, "notification_channels_list.json", &list)
	if len(list.Data) != 2 {
		t.Fatalf("list = %+v", list)
	}
	slack, https := list.Data[0], list.Data[1]
	if slack.Name != "team alerts" || slack.Kind != "slack" || slack.URLHint != "hooks.slack.com/…/abcd" || !slack.Enabled ||
		slack.DisabledReason != nil || slack.Signing != nil || len(slack.Subscriptions) != 4 ||
		slack.Subscriptions[1].Event != "run.failed" || slack.Subscriptions[1].AutomationID != nil ||
		*slack.LastDeliveryStatus != "delivered" || slack.UpdatedBy != "browser_session" {
		t.Fatalf("slack = %+v", slack)
	}
	if https.Enabled || *https.DisabledReason != "failing" || https.ConsecutiveFailures != 20 || https.Signing == nil ||
		*https.Signing.PreviousExpiresAt != "2026-10-15T09:00:00Z" ||
		*https.Subscriptions[0].AutomationID != "a0000000-0000-4000-8000-000000000001" || https.SigningSecret != nil {
		t.Fatalf("https = %+v", https)
	}

	var created NotificationChannel
	golden3(t, "notification_channel_create_https.json", &created)
	if created.SigningSecret == nil || !strings.HasPrefix(*created.SigningSecret, "nan_nsec_") || created.Signing == nil ||
		created.Signing.PreviousExpiresAt != nil || created.Kind != "https" {
		t.Fatalf("created = %+v", created)
	}

	var ds NotificationDeliveryList
	golden3(t, "notification_deliveries.json", &ds)
	if len(ds.Data) != 3 || ds.Data[0].Event != "digest" || ds.Data[0].Items != 12 || *ds.Data[0].HTTPStatus != 404 ||
		*ds.Data[0].Error != "http_4xx" || ds.Data[2].HTTPStatus != nil || *ds.Data[2].RunID == "" || ds.Data[2].Attempt != 2 {
		t.Fatalf("deliveries = %+v", ds.Data)
	}

	var tr NotificationTestResult
	golden3(t, "notification_test_failed.json", &tr)
	if tr.Delivered || tr.Status != "failed" || *tr.HTTPStatus != 404 || *tr.Error != "http_4xx" {
		t.Fatalf("test = %+v", tr)
	}

	var rot NotificationRotation
	golden3(t, "notification_rotate.json", &rot)
	if !strings.HasPrefix(rot.SigningSecret, "nan_nsec_") || *rot.PreviousExpiresAt != "2026-10-15T12:00:00Z" {
		t.Fatalf("rotate = %+v", rot)
	}
}

const notifURL = "https://hooks.slack.com/services/T000/B000/XXXXsecretpartXXXX"

// A webhook URL is a credential: it goes in the request body and nowhere
// a %v, %+v or %#v could print it.
func TestNotificationRequestsRedactTheURL(t *testing.T) {
	u := notifURL
	name := "n"
	for _, v := range []any{
		CreateNotificationChannelRequest{Name: "n", Kind: "slack", URL: notifURL},
		NotificationChannelPatch{Name: &name, URL: &u},
	} {
		for _, f := range []string{"%v", "%+v", "%#v", "%s"} {
			if s := fmt.Sprintf(f, v); strings.Contains(s, "secretpart") {
				t.Errorf("%s prints the URL: %s", f, s)
			}
		}
	}
	b, _ := json.Marshal(CreateNotificationChannelRequest{Name: "n", Kind: "slack", URL: notifURL})
	if string(b) != `{"name":"n","kind":"slack","url":"`+notifURL+`"}` {
		t.Errorf("create body %s", b)
	}
	none := []string{}
	aut := "a1"
	b, _ = json.Marshal(CreateNotificationChannelRequest{Name: "n", Kind: "https", URL: "u", Events: &none, AutomationID: &aut})
	if string(b) != `{"name":"n","kind":"https","url":"u","events":[],"automation_id":"a1"}` {
		t.Errorf("create body %s", b)
	}
	on := true
	b, _ = json.Marshal(NotificationChannelPatch{Enabled: &on})
	if string(b) != `{"enabled":true}` {
		t.Errorf("patch body %s", b)
	}
	b, _ = json.Marshal(notificationSubscriptionsBody{Subscriptions: []NotificationSubscription{{Event: "run.failed"}}})
	if string(b) != `{"subscriptions":[{"event":"run.failed","automation_id":null}]}` {
		t.Errorf("subscriptions body %s", b)
	}
	b, _ = json.Marshal(notificationSubscriptionsBody{Subscriptions: []NotificationSubscription{}})
	if string(b) != `{"subscriptions":[]}` {
		t.Errorf("empty subscriptions body %s", b)
	}
}

// Every route of /v1/notification-channels, with the method and body the
// platform expects.
func TestNotificationChannelRoutes(t *testing.T) {
	const id = "c0000000-0000-4000-8000-000000000001"
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got = append(got, r.Method+" "+r.URL.RequestURI()+" "+string(body))
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodDelete:
			w.WriteHeader(204)
		case strings.HasSuffix(r.URL.Path, "/deliveries"):
			_, _ = io.WriteString(w, `{"object":"list","data":[]}`)
		case strings.HasSuffix(r.URL.Path, "/test"):
			_, _ = io.WriteString(w, `{"delivered":true,"status":"delivered","http_status":200,"error":null}`)
		case strings.HasSuffix(r.URL.Path, "/rotate-signing-secret"):
			_, _ = io.WriteString(w, `{"signing_secret":"nan_nsec_x","previous_expires_at":"2026-10-15T12:00:00Z"}`)
		case r.URL.Path == "/v1/notification-channels" && r.Method == http.MethodGet:
			_, _ = io.WriteString(w, `{"object":"list","data":[]}`)
		default:
			_, _ = io.WriteString(w, `{"id":"`+id+`","kind":"slack","subscriptions":[]}`)
		}
	}))
	defer srv.Close()
	c := NewRunsClient("", "nan_pat_x").WithBaseURL(srv.URL)
	ctx := context.Background()
	on := false
	steps := []func() error{
		func() error { _, err := c.ListNotificationChannels(ctx); return err },
		func() error { _, err := c.NotificationDeliveries(ctx, id, 7); return err },
		func() error {
			_, err := c.CreateNotificationChannel(ctx, CreateNotificationChannelRequest{Name: "a", Kind: "slack", URL: "u"})
			return err
		},
		func() error {
			_, err := c.UpdateNotificationChannel(ctx, id, NotificationChannelPatch{Enabled: &on})
			return err
		},
		func() error { return c.DeleteNotificationChannel(ctx, id) },
		func() error {
			_, err := c.PutNotificationSubscriptions(ctx, id, []NotificationSubscription{{Event: "run.failed"}})
			return err
		},
		func() error { _, err := c.TestNotificationChannel(ctx, id); return err },
		func() error { _, err := c.RotateNotificationSigningSecret(ctx, id); return err },
	}
	for i, s := range steps {
		if err := s(); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
	}
	p := "/v1/notification-channels"
	want := []string{
		"GET " + p + " ",
		"GET " + p + "/" + id + "/deliveries?limit=7 ",
		"POST " + p + ` {"name":"a","kind":"slack","url":"u"}`,
		"PATCH " + p + "/" + id + ` {"enabled":false}`,
		"DELETE " + p + "/" + id + " ",
		"PUT " + p + "/" + id + `/subscriptions {"subscriptions":[{"event":"run.failed","automation_id":null}]}`,
		"POST " + p + "/" + id + "/test ",
		"POST " + p + "/" + id + "/rotate-signing-secret ",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("requests:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// The portal's flat error and the /v1 envelope both carry code and param.
func TestNotificationErrorsCarryCodeAndParam(t *testing.T) {
	for _, body := range []string{
		`{"error":{"message":"the URL is not valid","type":"invalid_request_error","param":"url","code":"invalid_url"}}`,
		`{"error":"invalid_url","message":"the URL is not valid","param":"url"}`,
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(400)
			_, _ = io.WriteString(w, body)
		}))
		c := NewRunsClient("", "nan_pat_x").WithBaseURL(srv.URL)
		_, err := c.CreateNotificationChannel(context.Background(), CreateNotificationChannelRequest{Name: "a", Kind: "slack", URL: notifURL})
		srv.Close()
		apiErr, ok := err.(*APIError)
		if !ok || apiErr.Code != "invalid_url" || apiErr.Param != "url" || apiErr.Status != 400 {
			t.Fatalf("%T %+v", err, err)
		}
		if strings.Contains(err.Error(), "secretpart") {
			t.Error("the error carries the URL")
		}
	}
}
