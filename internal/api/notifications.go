package api

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// Notification channels (helmcode/nan-devops
// AGENT-AUTOMATIONS-PHASE3-NOTIFICATIONS.md §4): where the platform tells a
// member that an automation run needs approval, failed or finished, that an
// automation was disabled or that a webhook trigger keeps failing. A
// channel's URL is a credential: it goes out in the body of a create or a
// URL replace and never comes back (url_hint is all the platform shows).
// An https channel's signing secret is answered once, by create and by
// rotate.

const notificationChannelsPath = "/v1/notification-channels"

// The kinds of channel.
const (
	NotificationKindSlack   = "slack"
	NotificationKindTeams   = "teams"
	NotificationKindDiscord = "discord"
	NotificationKindHTTPS   = "https"
)

// NotificationEvents are the events a channel can subscribe to, in the
// order the platform documents them.
var NotificationEvents = []string{"approval.pending", "run.failed", "run.finished", "automation.disabled", "trigger.failing"}

// DefaultNotificationEvents are what a new channel gets when no events are
// given (run.finished is opt-in).
var DefaultNotificationEvents = []string{"approval.pending", "run.failed", "automation.disabled", "trigger.failing"}

// NotificationSubscription is one event of a channel, for every automation
// of the member (AutomationID nil) or for one.
type NotificationSubscription struct {
	Event        string  `json:"event"`
	AutomationID *string `json:"automation_id"`
}

// NotificationSigning is the signing state of an https channel.
type NotificationSigning struct {
	PreviousExpiresAt *string `json:"previous_expires_at"`
}

// NotificationChannel is the channel object. SigningSecret is set only in
// the answer to the create of an https channel.
type NotificationChannel struct {
	ID                  string                     `json:"id"`
	Object              string                     `json:"object,omitempty"`
	Name                string                     `json:"name"`
	Kind                string                     `json:"kind"`
	URLHint             string                     `json:"url_hint"`
	Enabled             bool                       `json:"enabled"`
	DisabledReason      *string                    `json:"disabled_reason"`
	ConsecutiveFailures int                        `json:"consecutive_failures"`
	LastDeliveryAt      *string                    `json:"last_delivery_at"`
	LastDeliveryStatus  *string                    `json:"last_delivery_status"`
	Subscriptions       []NotificationSubscription `json:"subscriptions"`
	Signing             *NotificationSigning       `json:"signing"`
	UpdatedBy           string                     `json:"updated_by,omitempty"`
	CreatedAt           string                     `json:"created_at"`
	UpdatedAt           string                     `json:"updated_at"`
	SigningSecret       *string                    `json:"signing_secret,omitempty"`
}

type NotificationChannelList struct {
	Data []NotificationChannel `json:"data"`
}

// NotificationDelivery is one attempt (or drop) the platform recorded.
type NotificationDelivery struct {
	ID           string  `json:"id"`
	Object       string  `json:"object,omitempty"`
	Event        string  `json:"event"`
	Items        int     `json:"items"`
	RunID        *string `json:"run_id"`
	AutomationID *string `json:"automation_id"`
	Attempt      int     `json:"attempt"`
	Status       string  `json:"status"`
	HTTPStatus   *int    `json:"http_status"`
	Error        *string `json:"error"`
	CreatedAt    string  `json:"created_at"`
}

type NotificationDeliveryList struct {
	Data []NotificationDelivery `json:"data"`
}

// NotificationTestResult is the answer to POST …/{id}/test.
type NotificationTestResult struct {
	Delivered  bool    `json:"delivered"`
	Status     string  `json:"status"`
	HTTPStatus *int    `json:"http_status"`
	Error      *string `json:"error"`
}

// NotificationRotation is the answer to POST …/{id}/rotate-signing-secret.
type NotificationRotation struct {
	SigningSecret     string  `json:"signing_secret"`
	PreviousExpiresAt *string `json:"previous_expires_at"`
}

// CreateNotificationChannelRequest is the body of a create. Its URL never
// prints: String and GoString redact it.
type CreateNotificationChannelRequest struct {
	Name         string    `json:"name"`
	Kind         string    `json:"kind"`
	URL          string    `json:"url"`
	Events       *[]string `json:"events,omitempty"`
	AutomationID *string   `json:"automation_id,omitempty"`
}

func (r CreateNotificationChannelRequest) String() string {
	return fmt.Sprintf("CreateNotificationChannelRequest{Name:%q Kind:%q URL:[redacted]}", r.Name, r.Kind)
}
func (r CreateNotificationChannelRequest) GoString() string { return r.String() }

// NotificationChannelPatch is the body of PATCH …/{id}: nil fields are
// left as they are. Its URL never prints.
type NotificationChannelPatch struct {
	Name    *string `json:"name,omitempty"`
	Enabled *bool   `json:"enabled,omitempty"`
	URL     *string `json:"url,omitempty"`
}

func (p NotificationChannelPatch) String() string {
	name := "<nil>"
	if p.Name != nil {
		name = strconv.Quote(*p.Name)
	}
	enabled := "<nil>"
	if p.Enabled != nil {
		enabled = strconv.FormatBool(*p.Enabled)
	}
	return fmt.Sprintf("NotificationChannelPatch{Name:%s Enabled:%s URL:%s}", name, enabled, redactedIf(p.URL != nil))
}
func (p NotificationChannelPatch) GoString() string { return p.String() }

type notificationSubscriptionsBody struct {
	Subscriptions []NotificationSubscription `json:"subscriptions"`
}

func channelPath(id string) string { return notificationChannelsPath + "/" + url.PathEscape(id) }

// ListNotificationChannels is GET /v1/notification-channels.
func (c *RunsClient) ListNotificationChannels(ctx context.Context) (*NotificationChannelList, error) {
	list := &NotificationChannelList{}
	return list, c.getJSON(ctx, notificationChannelsPath, list)
}

// NotificationDeliveries is GET …/{id}/deliveries?limit=N (newest first).
func (c *RunsClient) NotificationDeliveries(ctx context.Context, id string, limit int) (*NotificationDeliveryList, error) {
	list := &NotificationDeliveryList{}
	return list, c.getJSON(ctx, channelPath(id)+"/deliveries?limit="+strconv.Itoa(limit), list)
}

// CreateNotificationChannel is POST /v1/notification-channels. Not
// idempotent: after a lost answer, list the channels before trying again.
func (c *RunsClient) CreateNotificationChannel(ctx context.Context, in CreateNotificationChannelRequest) (*NotificationChannel, error) {
	ch := &NotificationChannel{}
	return ch, c.sendJSON(ctx, http.MethodPost, notificationChannelsPath, in, ch)
}

// UpdateNotificationChannel is PATCH …/{id}.
func (c *RunsClient) UpdateNotificationChannel(ctx context.Context, id string, p NotificationChannelPatch) (*NotificationChannel, error) {
	ch := &NotificationChannel{}
	return ch, c.sendJSON(ctx, http.MethodPatch, channelPath(id), p, ch)
}

// DeleteNotificationChannel is DELETE …/{id}.
func (c *RunsClient) DeleteNotificationChannel(ctx context.Context, id string) error {
	return c.sendJSON(ctx, http.MethodDelete, channelPath(id), nil, nil)
}

// PutNotificationSubscriptions is PUT …/{id}/subscriptions: subs replaces
// the channel's whole set (empty removes every subscription).
func (c *RunsClient) PutNotificationSubscriptions(ctx context.Context, id string, subs []NotificationSubscription) (*NotificationChannel, error) {
	if subs == nil {
		subs = []NotificationSubscription{}
	}
	ch := &NotificationChannel{}
	return ch, c.sendJSON(ctx, http.MethodPut, channelPath(id)+"/subscriptions", notificationSubscriptionsBody{Subscriptions: subs}, ch)
}

// TestNotificationChannel is POST …/{id}/test: one test notification, sent
// now.
func (c *RunsClient) TestNotificationChannel(ctx context.Context, id string) (*NotificationTestResult, error) {
	out := &NotificationTestResult{}
	return out, c.sendJSON(ctx, http.MethodPost, channelPath(id)+"/test", nil, out)
}

// RotateNotificationSigningSecret is POST …/{id}/rotate-signing-secret.
func (c *RunsClient) RotateNotificationSigningSecret(ctx context.Context, id string) (*NotificationRotation, error) {
	out := &NotificationRotation{}
	return out, c.sendJSON(ctx, http.MethodPost, channelPath(id)+"/rotate-signing-secret", nil, out)
}
