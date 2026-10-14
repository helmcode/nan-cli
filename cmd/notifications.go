package cmd

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/nxssie/nan-cli/internal/api"
	"github.com/nxssie/nan-cli/internal/runs"
	"github.com/spf13/cobra"
)

// Notification channels (helmcode/nan-devops
// AGENT-AUTOMATIONS-PHASE3-NOTIFICATIONS.md): Slack, Microsoft Teams,
// Discord or any HTTPS endpoint, told when an automation run needs approval,
// failed or finished, when an automation is disabled and when a webhook
// trigger keeps failing. Everything goes through /v1/notification-channels:
// a CLI session, or a platform token with notifications:read (reads) or
// notifications:write (changes).
//
// A channel's URL is a credential (whoever has it can post to the channel):
// it is read from a file or a pipe, sent in the body of a create or a
// replace, and never printed; the platform only ever shows url_hint. An
// https channel's signing secret is printed once.

const (
	notificationsDocs = "https://nan.builders/docs/notifications"
	notificationsURL  = runs.PortalURL + "/settings"

	scopeNotificationsRead  = "notifications:read"
	scopeNotificationsWrite = "notifications:write"

	// maxHookURL is the platform's limit on a channel URL.
	maxHookURL = 2048

	// maxDeliveriesLimit is the platform's cap on ?limit=.
	maxDeliveriesLimit = 100
)

// notifSecretNote goes to stderr after a signing secret was printed.
const notifSecretNote = "Store it now: it is never shown again. Lost it? Issue a new one with: nan notifications rotate"

var (
	notifJSON bool

	notifAddKind     string
	notifAddEvents   []string
	notifAddNoEvents bool
	notifAddAuto     string

	notifURLFile  string
	notifURLStdin bool
	notifURLArg   string

	notifEditName    string
	notifEditEnable  bool
	notifEditDisable bool

	notifRmYes bool

	notifDeliveriesLimit int

	notifSubsSet    []string
	notifSubsAdd    []string
	notifSubsRemove []string
	notifSubsClear  bool
)

var notifVerbHints = map[string]string{
	"create": "add", "new": "add", "list": "ls", "get": "show", "update": "edit", "enable": "edit NAME --enable",
	"disable": "edit NAME --disable", "rotate-secret": "rotate", "events": "subscriptions", "subs": "subscriptions",
	"logs": "deliveries", "history": "deliveries", "ping": "test", "send": "test",
}

const notifCredentialHelp = `Authentication, first match wins: --token-file, your session from
nan auth login, NAN_TOKEN, a platform token saved with nan auth login
--api-token. A platform token needs notifications:read to list and show
channels, their deliveries and subscriptions, and notifications:write to
change them (finding a channel by NAME needs notifications:read too;
naming an automation needs automations:read). Those scopes are added to a
token in the portal only, Settings > Tokens, signed in through the
browser. API keys (sk-) and workspace keys never manage notifications.`

var notificationsCmd = &cobra.Command{
	Use:   "notifications",
	Short: "Send automation events to Slack, Teams, Discord or your own HTTPS endpoint",
	Long: `Manage where NaN tells you about your automations: a Slack, Microsoft
Teams or Discord channel (through its incoming webhook URL), or your own
HTTPS endpoint (signed, like NaN's inbound webhooks).

Events: approval.pending (a run waits for your approval), run.failed
(failed or timed out), automation.disabled, trigger.failing (a webhook
trigger keeps getting bad signatures) and run.finished. A new channel gets
all of them except run.finished, which is opt-in. Only automation runs
notify. Many notifications at once arrive as one digest.

A webhook URL is a credential: whoever has it can post to your channel.
It is read from a file or a pipe, never from the command line, where it
would stay in your shell history:
  nan notifications add team-alerts --kind slack --url-file ./slack-url.txt
  pbpaste | nan notifications add ci --kind https --url-stdin
NaN never shows it again: you see the host and its last characters.

After 20 failed deliveries in a row a channel is disabled; fix the
receiver (or replace the URL) and turn it back on with:
  nan notifications edit NAME --enable

` + notifCredentialHelp + `

The portal does all of this too, Settings > Notifications at ` + notificationsURL + `.

Docs: ` + notificationsDocs,
	Annotations: sysexits,
	Args:        cobra.ArbitraryArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 {
			if looksLikeURL(args[0]) {
				return urlOnCommandLine()
			}
			shown := runs.Truncate(runs.SanitizeLine(args[0]), 64)
			if hint, ok := notifVerbHints[args[0]]; ok {
				return usageErrorf("there is no nan notifications %s; try: nan notifications %s", shown, hint)
			}
			return usageErrorf("unknown command %q for \"nan notifications\"; try: nan notifications --help", shown)
		}
		return cmd.Help()
	},
}

var notifLsCmd = &cobra.Command{
	Use:         "ls",
	Short:       "List your notification channels",
	Args:        cobra.NoArgs,
	Annotations: sysexits,
	RunE: func(cmd *cobra.Command, args []string) error {
		return withWriteEnv(func(env *runsEnv) error { return doNotifLs(cmd.Context(), env) })
	},
}

var notifShowCmd = &cobra.Command{
	Use:         "show <NAME|id>",
	Short:       "Show one channel: destination, status, subscriptions",
	Args:        oneChannel,
	Annotations: sysexits,
	RunE: func(cmd *cobra.Command, args []string) error {
		return withWriteEnv(func(env *runsEnv) error { return doNotifShow(cmd.Context(), env, args[0]) })
	},
}

var notifAddCmd = &cobra.Command{
	Use:   "add NAME --kind slack|teams|discord|https (--url-file PATH | --url-stdin)",
	Short: "Add a channel; an https channel's signing secret is shown once",
	Long: `Add a notification channel.

--kind is slack, teams, discord or https, fixed once the channel exists.
The URL comes from --url-file PATH or a pipe with --url-stdin, never from
the command line. Where to get it:
  slack    an incoming webhook (api.slack.com/apps > your app > Incoming
           Webhooks): https://hooks.slack.com/services/...
  teams    a Workflows "When a Teams webhook request is received" flow, or
           an incoming webhook connector: https://...webhook.office.com/...,
           ...logic.azure.com/... or ...powerplatform.com/...
  discord  Server Settings > Integrations > Webhooks > Copy Webhook URL:
           https://discord.com/api/webhooks/...
  https    your own endpoint on port 443; NaN signs every request
           (X-Nan-Signature, the same scheme as NaN's inbound webhooks)

Events: --event, repeated or comma-separated, out of approval.pending,
run.failed, run.finished, automation.disabled and trigger.failing. Without
it the channel gets every event but run.finished; --no-events adds it with
none (set them later with nan notifications subscriptions). --automation
limits the events to one automation (a name or an id).

An https channel's signing secret is printed once, to stdout (with --json,
as signing_secret). Store it then: it is never shown again.

Up to 10 channels per account.

` + notifCredentialHelp + `

Docs: ` + notificationsDocs,
	Example: `  nan notifications add team-alerts --kind slack --url-file ./slack-url.txt
  pbpaste | nan notifications add deploys --kind discord --url-stdin --event run.failed,run.finished
  nan notifications add ci --kind https --url-file url.txt --automation "PR review" --json`,
	Args:        oneChannelName,
	Annotations: sysexits,
	RunE: func(cmd *cobra.Command, args []string) error {
		in, err := buildNotifAdd(cmd, args[0])
		if err != nil {
			return err
		}
		return withWriteEnv(func(env *runsEnv) error { return doNotifAdd(cmd.Context(), env, in) })
	},
}

var notifEditCmd = &cobra.Command{
	Use:   "edit <NAME|id> [--name NEW] [--enable | --disable] [--url-file PATH | --url-stdin]",
	Short: "Rename a channel, turn it on or off, or replace its URL",
	Long: `Rename a channel, turn it on or off, or replace its URL (from --url-file
PATH or --url-stdin, never the command line). The kind cannot change.

--enable turns on a channel you disabled or the platform disabled after
repeated failures, and resets its failure count. Replacing the URL resets
the count too and turns back on a channel the platform disabled; one you
disabled yourself stays off until --enable.

` + notifCredentialHelp + `

Docs: ` + notificationsDocs,
	Example: `  nan notifications edit team-alerts --enable
  nan notifications edit team-alerts --url-file ./new-url.txt
  nan notifications edit team-alerts --name "team alerts" --disable`,
	Args:        oneChannel,
	Annotations: sysexits,
	RunE: func(cmd *cobra.Command, args []string) error {
		if notifURLArg != "" || cmd.Flags().Changed("url") {
			return urlOnCommandLine()
		}
		var p api.NotificationChannelPatch
		if cmd.Flags().Changed("name") {
			if err := checkChannelName(notifEditName); err != nil {
				return err
			}
			name := notifEditName
			p.Name = &name
		}
		switch {
		case notifEditEnable && notifEditDisable:
			return usageErrorf("give --enable or --disable, not both")
		case notifEditEnable, notifEditDisable:
			on := notifEditEnable
			p.Enabled = &on
		}
		if notifURLFile != "" || notifURLStdin {
			u, err := readHookURL()
			if err != nil {
				return err
			}
			p.URL = &u
		}
		if p.Name == nil && p.Enabled == nil && p.URL == nil {
			return usageErrorf("nothing to change: give --name, --enable, --disable, --url-file or --url-stdin")
		}
		return withWriteEnv(func(env *runsEnv) error { return doNotifEdit(cmd.Context(), env, args[0], p) })
	},
}

var notifRmCmd = &cobra.Command{
	Use:     "rm <NAME|id> [--yes]",
	Aliases: []string{"delete", "remove"},
	Short:   "Delete a channel",
	Long: `Delete a channel: it stops receiving notifications at once, and its
delivery log goes with it. Asks before deleting on a terminal; elsewhere,
confirm with --yes.

` + notifCredentialHelp + `

Docs: ` + notificationsDocs,
	Args:        oneChannel,
	Annotations: sysexits,
	RunE: func(cmd *cobra.Command, args []string) error {
		return withWriteEnv(func(env *runsEnv) error { return doNotifRm(cmd.Context(), env, args[0]) })
	},
}

var notifTestCmd = &cobra.Command{
	Use:   "test <NAME|id>",
	Short: "Send a test notification now (exit 1 when it is not delivered)",
	Long: `Send a test notification to a channel now and say how it went: exit 0
when it was delivered, 1 when it was not (with the reason). A disabled
channel is not tested: turn it on first. Up to 5 tests in a row, then one a
minute.

` + notifCredentialHelp + `

Docs: ` + notificationsDocs,
	Args:        oneChannel,
	Annotations: sysexits,
	RunE: func(cmd *cobra.Command, args []string) error {
		return withWriteEnv(func(env *runsEnv) error { return doNotifTest(cmd.Context(), env, args[0]) })
	},
}

var notifRotateCmd = &cobra.Command{
	Use:   "rotate <NAME|id>",
	Short: "Issue a new signing secret for an https channel, shown once",
	Long: `Issue a new signing secret for an https channel. It is printed once, to
stdout (with --json, as signing_secret). For 24 hours every request carries
both signatures (the new one first), so you can update your receiver; then
only the new one.

` + notifCredentialHelp + `

Docs: ` + notificationsDocs,
	Args:        oneChannel,
	Annotations: sysexits,
	RunE: func(cmd *cobra.Command, args []string) error {
		return withWriteEnv(func(env *runsEnv) error { return doNotifRotate(cmd.Context(), env, args[0]) })
	},
}

var notifDeliveriesCmd = &cobra.Command{
	Use:   "deliveries <NAME|id> [--limit N]",
	Short: "The channel's recent delivery attempts, newest first",
	Long: `The channel's recent delivery attempts, newest first (kept 30 days): the
event, how it went, the HTTP status and the error class. Never a payload,
a URL or a response body.

` + notifCredentialHelp + `

Docs: ` + notificationsDocs,
	Args:        oneChannel,
	Annotations: sysexits,
	RunE: func(cmd *cobra.Command, args []string) error {
		if notifDeliveriesLimit < 1 || notifDeliveriesLimit > maxDeliveriesLimit {
			return usageErrorf("--limit is 1 to %d", maxDeliveriesLimit)
		}
		return withWriteEnv(func(env *runsEnv) error { return doNotifDeliveries(cmd.Context(), env, args[0]) })
	},
}

var notifSubsCmd = &cobra.Command{
	Use:   "subscriptions <NAME|id> [--set|--add|--remove EVENT[=AUTOMATION]]... [--clear]",
	Short: "List or change which events a channel receives",
	Long: `Without flags, list the events a channel receives. With them, change the
list:
  --add EVENT[=AUTOMATION]     receive EVENT (repeat for more)
  --remove EVENT[=AUTOMATION]  stop receiving it
  --set EVENT[=AUTOMATION]     replace the whole list with these
  --clear                      receive nothing
EVENT alone is for every automation; EVENT=AUTOMATION (a name or an id) for
that one only. Events: approval.pending, run.failed, run.finished,
automation.disabled, trigger.failing. Up to 50 per channel.

With --json, listing prints {"subscriptions": [...]}; a change prints the
whole channel as the platform answers it.

` + notifCredentialHelp + `

Docs: ` + notificationsDocs,
	Example: `  nan notifications subscriptions team-alerts
  nan notifications subscriptions team-alerts --add run.finished="PR review"
  nan notifications subscriptions team-alerts --set run.failed --set approval.pending`,
	Args:        oneChannel,
	Annotations: sysexits,
	RunE: func(cmd *cobra.Command, args []string) error {
		changes := 0
		for _, n := range []int{len(notifSubsSet), len(notifSubsAdd) + len(notifSubsRemove)} {
			if n > 0 {
				changes++
			}
		}
		if notifSubsClear {
			changes++
		}
		if changes > 1 {
			return usageErrorf("give --set, --add/--remove or --clear: --set and --clear replace the whole list, --add and --remove change it")
		}
		for _, list := range [][]string{notifSubsSet, notifSubsAdd, notifSubsRemove} {
			for _, s := range list {
				if _, _, err := parseSubscriptionArg(s); err != nil {
					return err
				}
			}
		}
		return withWriteEnv(func(env *runsEnv) error { return doNotifSubs(cmd.Context(), env, args[0], changes > 0) })
	},
}

func init() {
	rootCmd.AddCommand(notificationsCmd)
	notificationsCmd.AddCommand(notifLsCmd, notifShowCmd, notifAddCmd, notifEditCmd, notifRmCmd, notifTestCmd,
		notifRotateCmd, notifDeliveriesCmd, notifSubsCmd)
	notificationsCmd.PersistentFlags().StringVar(&tokenFileFlag, "token-file", "", tokenFileUsage)

	for _, c := range []*cobra.Command{notifLsCmd, notifShowCmd, notifAddCmd, notifEditCmd, notifTestCmd, notifRotateCmd,
		notifDeliveriesCmd, notifSubsCmd} {
		c.Flags().BoolVar(&notifJSON, "json", false, "Print the answer as JSON")
	}
	for _, c := range []*cobra.Command{notifAddCmd, notifEditCmd} {
		c.Flags().StringVar(&notifURLFile, "url-file", "", "Read the webhook URL from `PATH`")
		c.Flags().BoolVar(&notifURLStdin, "url-stdin", false, "Read the webhook URL from stdin (a pipe, never a terminal)")
		// --url exists only to refuse it with a reason: a URL typed on the
		// command line stays in the shell history.
		c.Flags().StringVar(&notifURLArg, "url", "", "Not accepted: use --url-file or --url-stdin")
		_ = c.Flags().MarkHidden("url")
	}
	f := notifAddCmd.Flags()
	f.StringVar(&notifAddKind, "kind", "", "slack, teams, discord or https")
	f.StringSliceVar(&notifAddEvents, "event", nil, "An event to receive (repeat, or comma-separate); default: all but run.finished")
	f.BoolVar(&notifAddNoEvents, "no-events", false, "Add the channel with no events")
	f.StringVar(&notifAddAuto, "automation", "", "Only this automation's events (a name or an id)")

	notifEditCmd.Flags().StringVar(&notifEditName, "name", "", "The new name")
	notifEditCmd.Flags().BoolVar(&notifEditEnable, "enable", false, "Turn the channel on (resets its failure count)")
	notifEditCmd.Flags().BoolVar(&notifEditDisable, "disable", false, "Turn the channel off")

	notifRmCmd.Flags().BoolVar(&notifRmYes, "yes", false, "Delete without asking")

	notifDeliveriesCmd.Flags().IntVar(&notifDeliveriesLimit, "limit", 20, "How many attempts to show (1 to 100)")

	s := notifSubsCmd.Flags()
	s.StringArrayVar(&notifSubsSet, "set", nil, "Replace the list with EVENT[=AUTOMATION] (repeat)")
	s.StringArrayVar(&notifSubsAdd, "add", nil, "Add EVENT[=AUTOMATION] (repeat)")
	s.StringArrayVar(&notifSubsRemove, "remove", nil, "Remove EVENT[=AUTOMATION] (repeat)")
	s.BoolVar(&notifSubsClear, "clear", false, "Remove every subscription")
}

// ─── arguments ─────────────────────────────────────────────────────────────

// looksLikeURL is an argument that is (or starts like) a webhook URL.
func looksLikeURL(s string) bool {
	l := strings.ToLower(strings.TrimSpace(s))
	return strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://") || strings.Contains(l, "hooks.slack.com") ||
		strings.Contains(l, "/api/webhooks/") || strings.Contains(l, "webhook.office.com")
}

// urlOnCommandLine is the refusal of a URL given as an argument. It never
// repeats the URL.
func urlOnCommandLine() *ExitError {
	return usageErrorf("a webhook URL is a credential: give it with --url-file PATH or --url-stdin (a pipe), never on the command line. " +
		"If you typed it, it is now in your shell history: delete that webhook at the provider, create a new one and add it from a file")
}

func oneChannel(cmd *cobra.Command, args []string) error {
	switch {
	case len(args) == 0:
		return usageErrorf("missing the channel's NAME or id; see: nan notifications ls")
	case looksLikeURL(args[0]) || (len(args) > 1 && looksLikeURL(args[1])):
		return urlOnCommandLine()
	case len(args) > 1:
		return usageErrorf("one channel at a time (quote a NAME with spaces: \"team alerts\")")
	}
	return nil
}

func oneChannelName(cmd *cobra.Command, args []string) error {
	switch {
	case len(args) == 0:
		return usageErrorf("missing the channel's name, like team-alerts")
	case looksLikeURL(args[0]) || (len(args) > 1 && looksLikeURL(args[len(args)-1])):
		return urlOnCommandLine()
	case len(args) > 1:
		return usageErrorf("nan notifications add takes one NAME (quote a name with spaces); the URL goes in --url-file or --url-stdin")
	}
	return checkChannelName(args[0])
}

// checkChannelName is the platform's rule: 1 to 64 characters, no control
// characters.
func checkChannelName(name string) error {
	n := len([]rune(name))
	if n == 0 || n > 64 || strings.ContainsFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) }) {
		return usageErrorf("a channel name is 1 to 64 characters, without control characters")
	}
	if looksLikeURL(name) {
		return urlOnCommandLine()
	}
	return nil
}

var urlInputFlags = secretInputFlags{"--url-file", "--url-stdin", "pbpaste | nan notifications add NAME --kind slack --url-stdin"}

// readHookURL reads the URL from --url-file or a piped --url-stdin. The
// platform validates it; errors never include it.
func readHookURL() (string, error) {
	switch {
	case notifURLFile != "" && notifURLStdin:
		return "", usageErrorf("give --url-file or --url-stdin, not both")
	case notifURLFile == "" && !notifURLStdin:
		return "", usageErrorf("the webhook URL comes from --url-file PATH or --url-stdin (a pipe), never from the command line")
	}
	u, err := readSecretInput(urlInputFlags, notifURLFile, notifURLStdin)
	if err != nil {
		var exit *ExitError
		if errors.As(err, &exit) {
			msg := strings.ReplaceAll(exit.Err.Error(), "the value", "the URL")
			exit.Err = errors.New(strings.ReplaceAll(msg, ", the most a secret holds", ""))
		}
		return "", err
	}
	u = strings.TrimSpace(u)
	switch {
	case u == "":
		return "", usageErrorf("the URL is empty")
	case len(u) > maxHookURL:
		return "", usageErrorf("the URL is over %d bytes, the most the platform takes", maxHookURL)
	}
	return u, nil
}

type notifAdd struct {
	req        api.CreateNotificationChannelRequest
	automation string
}

func buildNotifAdd(cmd *cobra.Command, name string) (*notifAdd, error) {
	if notifURLArg != "" || cmd.Flags().Changed("url") {
		return nil, urlOnCommandLine()
	}
	switch notifAddKind {
	case api.NotificationKindSlack, api.NotificationKindTeams, api.NotificationKindDiscord, api.NotificationKindHTTPS:
	case "":
		return nil, usageErrorf("--kind is required: slack, teams, discord or https")
	default:
		return nil, usageErrorf("--kind is slack, teams, discord or https")
	}
	in := &notifAdd{req: api.CreateNotificationChannelRequest{Name: name, Kind: notifAddKind}, automation: notifAddAuto}
	switch {
	case notifAddNoEvents && len(notifAddEvents) > 0:
		return nil, usageErrorf("give --event or --no-events, not both")
	case notifAddNoEvents:
		none := []string{}
		in.req.Events = &none
	case len(notifAddEvents) > 0:
		events := []string{}
		for _, e := range notifAddEvents {
			e = strings.TrimSpace(e)
			if err := checkEvent(e); err != nil {
				return nil, err
			}
			if !slices.Contains(events, e) {
				events = append(events, e)
			}
		}
		in.req.Events = &events
	}
	u, err := readHookURL()
	if err != nil {
		return nil, err
	}
	in.req.URL = u
	return in, nil
}

func checkEvent(e string) error {
	if slices.Contains(api.NotificationEvents, e) {
		return nil
	}
	return usageErrorf("%q is not an event: use %s", runs.Truncate(runs.SanitizeLine(e), 40), strings.Join(api.NotificationEvents, ", "))
}

// parseSubscriptionArg splits EVENT[=AUTOMATION] (an event never has '=').
func parseSubscriptionArg(s string) (event, automation string, err error) {
	event, automation, _ = strings.Cut(s, "=")
	event = strings.TrimSpace(event)
	if event == "" {
		return "", "", usageErrorf("a subscription is EVENT or EVENT=AUTOMATION, like run.failed or run.failed=\"PR review\"")
	}
	if err := checkEvent(event); err != nil {
		return "", "", err
	}
	return event, strings.TrimSpace(automation), nil
}

// ─── platform plumbing ─────────────────────────────────────────────────────

// notifOff is the answer when the platform has no notification channels for
// the member.
func notifOff() *ExitError {
	return usageErrorf("notifications are not available for your account yet. When they are, manage them here or in the portal, "+
		"Settings > Notifications at %s. See %s", notificationsURL, notificationsDocs)
}

// requireNotifications asks the platform, for a session, whether the
// member has notifications (features.notifications) and takes them from the
// CLI (features.write_api). A token has no /api/auth/me: the platform's
// 404 answers for it. When the question itself fails for another reason
// than an expired session, the request goes ahead and the platform decides.
func requireNotifications(ctx context.Context, env *runsEnv) error {
	if env.cred.kind != credSession {
		return nil
	}
	f, err := env.client.Features(ctx)
	if errors.Is(err, api.ErrSessionExpired) {
		return authError(env.cred)
	}
	if err != nil {
		return nil
	}
	if !f.Notifications {
		return notifOff()
	}
	if !f.WriteAPI {
		return usageErrorf("managing notification channels from the CLI is not available for your account yet: do it in the portal, "+
			"Settings > Notifications at %s, signed in through the browser. See %s", notificationsURL, notificationsDocs)
	}
	return nil
}

// notifOp is what a request was doing, for the messages of notifExit.
type notifOp struct {
	scope   string // the platform-token scope of the request
	channel string // the channel's name or id, when there is one
	kind    string // the channel's kind, when known
	test    bool
}

// notifExit maps a refused notification request onto an exit code and a
// message that says what to do. The platform's messages never carry a URL
// or a secret; they are sanitised like any other text from the network.
func notifExit(err error, cred credential, op notifOp) *ExitError {
	var apiErr *api.APIError
	if !errors.As(err, &apiErr) {
		return apiExit(err, cred)
	}
	msg := runs.SanitizeLine(apiErr.Message)
	name := shellArg(op.channel)
	switch apiErr.Code {
	case "invalid_url":
		return usageErrorf("the URL was refused: %s. %s", msg, kindURLHint(op.kind))
	case "name_taken":
		return usageErrorf("that name is already taken by another of your notification channels")
	case "too_many_channels":
		return usageErrorf("you have 10 notification channels, the most an account can keep: delete one first (nan notifications rm NAME)")
	case "automation_not_found":
		return usageErrorf("that automation was not found among yours; see: nan automations ls")
	case "channel_disabled":
		return usageErrorf("%s is disabled: turn it on first with: nan notifications edit %s --enable", runs.SanitizeLine(op.channel), name)
	case "signing_not_supported":
		return usageErrorf("only https channels have a signing secret")
	case "rate_limited":
		if op.test {
			return exitf(exitUnavailable, "too many test notifications recently: up to 5 in a row, then one a minute; try again shortly")
		}
		return exitf(exitUnavailable, "too many changes to your notification channels recently: try again later")
	case "notifications_unavailable":
		return exitf(exitUnavailable, "notifications are temporarily unavailable; try again in a few minutes")
	case "token_scope_denied":
		return exitf(exitAuth, "%s lacks a scope: %s. Create a token with the %s scope in the portal, Settings > Tokens at %s, "+
			"signed in through the browser (the notifications scopes can only be added there)", cred.describe(), msg, op.scope, runs.PortalURL)
	case "credential_not_allowed":
		if cred.kind == credSession {
			return exitf(exitAuth, "the platform does not take this session for notification channels: it is from an older sign-in. "+
				"Sign in again with: nan auth login")
		}
		return exitf(exitAuth, "%s cannot manage notification channels: use a CLI session (nan auth login) or a platform token with the %s scope",
			cred.describe(), op.scope)
	case "workspace_key_not_allowed":
		return exitf(exitAuth, "a workspace key never manages notification channels: use your own session (nan auth login) "+
			"or a platform token with the %s scope", op.scope)
	case "browser_session_required":
		return writeAPIOff("notification channels")
	case "not_found", "":
		if apiErr.Status != 404 {
			break
		}
		if op.channel == "" || apiErr.Message == "Not found." {
			// The mount itself answered: no notifications for the member.
			return notifOff()
		}
		return usageErrorf("no notification channel %s; see: nan notifications ls", runs.Truncate(runs.SanitizeLine(op.channel), 64))
	}
	return apiExit(err, cred)
}

// kindURLHint is what a URL of each kind looks like.
func kindURLHint(kind string) string {
	switch kind {
	case api.NotificationKindSlack:
		return "A Slack URL is an incoming webhook: https://hooks.slack.com/services/..."
	case api.NotificationKindTeams:
		return "A Teams URL is a Workflows or incoming webhook URL on webhook.office.com, logic.azure.com or powerplatform.com"
	case api.NotificationKindDiscord:
		return "A Discord URL is a channel webhook: https://discord.com/api/webhooks/..."
	case api.NotificationKindHTTPS:
		return "An https URL is https on port 443 with a public DNS name (no IP address, no nan.builders)"
	}
	return "See " + notificationsDocs
}

var plainShellArg = regexp.MustCompile(`^[A-Za-z0-9._@%+=:,/-]+$`)

// shellArg quotes a (sanitised) channel name for a command to copy.
func shellArg(s string) string {
	s = runs.SanitizeLine(s)
	if plainShellArg.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func listChannels(ctx context.Context, env *runsEnv) (*api.NotificationChannelList, error) {
	list, err := env.client.ListNotificationChannels(ctx)
	if err != nil {
		return nil, notifExit(err, env.cred, notifOp{scope: scopeNotificationsRead})
	}
	return list, nil
}

func findChannel(list *api.NotificationChannelList, ref string) *api.NotificationChannel {
	for i := range list.Data {
		if strings.EqualFold(list.Data[i].ID, ref) {
			return &list.Data[i]
		}
	}
	for i := range list.Data {
		if strings.EqualFold(list.Data[i].Name, ref) {
			return &list.Data[i]
		}
	}
	return nil
}

// resolveChannel finds a channel by NAME or id. forWrite lets a token
// without notifications:read go ahead with an id it cannot look up.
func resolveChannel(ctx context.Context, env *runsEnv, ref string, forWrite bool) (*api.NotificationChannel, error) {
	if err := requireNotifications(ctx, env); err != nil {
		return nil, err
	}
	list, err := env.client.ListNotificationChannels(ctx)
	if err != nil {
		var apiErr *api.APIError
		if forWrite && errors.As(err, &apiErr) && apiErr.Code == "token_scope_denied" {
			if runIDPattern.MatchString(ref) {
				return &api.NotificationChannel{ID: ref, Name: ref}, nil
			}
			return nil, exitf(exitAuth, "%s cannot find a channel by name without the %s scope: give the channel id instead, "+
				"or use a token with %s too", env.cred.describe(), scopeNotificationsRead, scopeNotificationsRead)
		}
		return nil, notifExit(err, env.cred, notifOp{scope: scopeNotificationsRead})
	}
	c := findChannel(list, ref)
	if c == nil {
		return nil, usageErrorf("no notification channel named %q; see: nan notifications ls", runs.Truncate(runs.SanitizeLine(ref), 64))
	}
	return c, nil
}

// automationNames maps automation ids to names, best effort: a token
// without automations:read still gets the ids.
func automationNames(ctx context.Context, env *runsEnv, subs []api.NotificationSubscription) map[string]string {
	need := false
	for _, s := range subs {
		need = need || s.AutomationID != nil
	}
	names := map[string]string{}
	if !need {
		return names
	}
	if list, err := env.client.ListAutomations(ctx); err == nil {
		for _, a := range list.Data {
			names[a.ID] = a.Name
		}
	}
	return names
}

// ─── output ────────────────────────────────────────────────────────────────

func channelStatus(c *api.NotificationChannel) string {
	if c.Enabled {
		if c.ConsecutiveFailures > 0 {
			return fmt.Sprintf("on, %d failed in a row", c.ConsecutiveFailures)
		}
		return "on"
	}
	if c.DisabledReason == nil {
		return "off"
	}
	switch *c.DisabledReason {
	case "member":
		return "off"
	case "failing":
		return "off: failing"
	case "invalid_url":
		return "off: URL refused"
	}
	return "off: " + runs.SanitizeLine(*c.DisabledReason)
}

// disabledHelp says why a channel is off and how to turn it back on.
func disabledHelp(c *api.NotificationChannel) string {
	if c.Enabled || c.DisabledReason == nil {
		return ""
	}
	name := shellArg(c.Name)
	switch *c.DisabledReason {
	case "member":
		return "turned off by you; turn it on with: nan notifications edit " + name + " --enable"
	case "failing":
		return fmt.Sprintf("turned off after %d failed deliveries in a row; fix the receiver (or replace the URL with --url-file) "+
			"and turn it on with: nan notifications edit %s --enable", c.ConsecutiveFailures, name)
	case "invalid_url":
		return "turned off: its host resolved to an address NaN does not send to; replace the URL with: nan notifications edit " +
			name + " --url-file PATH"
	}
	return ""
}

func lastDelivery(c *api.NotificationChannel) string {
	if c.LastDeliveryAt == nil {
		return "never"
	}
	s := localTime(*c.LastDeliveryAt)
	if c.LastDeliveryStatus != nil {
		s += " " + runs.SanitizeLine(*c.LastDeliveryStatus)
	}
	return s
}

// eventsSummary is the EVENTS column: how many events, for which
// automations.
func eventsSummary(subs []api.NotificationSubscription) string {
	if len(subs) == 0 {
		return "none"
	}
	events := map[string]bool{}
	autos := map[string]bool{}
	all := false
	for _, s := range subs {
		events[s.Event] = true
		if s.AutomationID == nil {
			all = true
		} else {
			autos[*s.AutomationID] = true
		}
	}
	scope := ""
	switch {
	case all && len(autos) > 0:
		scope = fmt.Sprintf("all automations + %d", len(autos))
	case all:
		scope = "all automations"
	default:
		scope = plural(len(autos), "automation")
	}
	return plural(len(events), "event") + ", " + scope
}

func plural(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return fmt.Sprintf("%d %ss", n, what)
}

func doNotifLs(ctx context.Context, env *runsEnv) error {
	if err := requireNotifications(ctx, env); err != nil {
		return err
	}
	list, err := listChannels(ctx, env)
	if err != nil {
		return err
	}
	if notifJSON {
		return printJSON(env.stdout, list, true)
	}
	if len(list.Data) == 0 {
		fmt.Fprintln(env.stderr, "No notification channels yet. Add one with: nan notifications add NAME --kind slack --url-file PATH")
		return nil
	}
	tw := tabwriter.NewWriter(env.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tKIND\tDESTINATION\tSTATUS\tEVENTS\tLAST DELIVERY\tCHANGED FROM")
	var off []string
	for i := range list.Data {
		c := &list.Data[i]
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", runs.Truncate(runs.SanitizeLine(c.Name), 64), runs.SanitizeLine(c.Kind),
			runs.Truncate(runs.SanitizeLine(c.URLHint), 60), channelStatus(c), eventsSummary(c.Subscriptions), lastDelivery(c),
			dashIfEmpty(updatedByLabel(c.UpdatedBy)))
		if h := disabledHelp(c); h != "" && *c.DisabledReason != "member" {
			off = append(off, runs.SanitizeLine(c.Name)+": "+h)
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	for _, o := range off {
		fmt.Fprintln(env.stderr, o)
	}
	return nil
}

func doNotifShow(ctx context.Context, env *runsEnv, ref string) error {
	c, err := resolveChannel(ctx, env, ref, false)
	if err != nil {
		return err
	}
	if notifJSON {
		return printJSON(env.stdout, c, true)
	}
	printChannel(ctx, env, c)
	return nil
}

func printChannel(ctx context.Context, env *runsEnv, c *api.NotificationChannel) {
	tw := tabwriter.NewWriter(env.stdout, 0, 0, 2, ' ', 0)
	row := func(k, v string) {
		if v != "" {
			fmt.Fprintf(tw, "%s\t%s\n", k, v)
		}
	}
	row("name", runs.SanitizeLine(c.Name))
	row("id", runs.SanitizeLine(c.ID))
	row("kind", runs.SanitizeLine(c.Kind))
	row("destination", runs.SanitizeLine(c.URLHint))
	row("status", channelStatus(c))
	row("why", disabledHelp(c))
	row("last delivery", lastDelivery(c))
	if c.Signing != nil {
		signing := "one secret (rotate with: nan notifications rotate " + shellArg(c.Name) + ")"
		if c.Signing.PreviousExpiresAt != nil {
			signing = "two secrets: the previous one works until " + localTime(*c.Signing.PreviousExpiresAt)
		}
		row("signing", signing)
	}
	row("last changed from", updatedByLabel(c.UpdatedBy))
	row("created", localTime(c.CreatedAt))
	row("updated", localTime(c.UpdatedAt))
	_ = tw.Flush()
	fmt.Fprintln(env.stdout)
	printSubscriptions(env, c.Subscriptions, automationNames(ctx, env, c.Subscriptions))
}

func printSubscriptions(env *runsEnv, subs []api.NotificationSubscription, names map[string]string) {
	if len(subs) == 0 {
		fmt.Fprintln(env.stdout, "No subscriptions: this channel receives nothing.")
		return
	}
	tw := tabwriter.NewWriter(env.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "EVENT\tAUTOMATION")
	for _, s := range subs {
		auto := "all automations"
		if s.AutomationID != nil {
			auto = runs.SanitizeLine(*s.AutomationID)
			if n, ok := names[*s.AutomationID]; ok {
				auto = runs.Truncate(runs.SanitizeLine(n), 64) + " (" + auto + ")"
			}
		}
		fmt.Fprintf(tw, "%s\t%s\n", runs.SanitizeLine(s.Event), auto)
	}
	_ = tw.Flush()
}

func doNotifAdd(ctx context.Context, env *runsEnv, in *notifAdd) error {
	if err := requireNotifications(ctx, env); err != nil {
		return err
	}
	if in.automation != "" {
		id, err := resolveAutomation(ctx, env, in.automation)
		if err != nil {
			return err
		}
		in.req.AutomationID = &id
	}
	c, err := env.client.CreateNotificationChannel(ctx, in.req)
	if err != nil {
		return notifExit(err, env.cred, notifOp{scope: scopeNotificationsWrite, kind: in.req.Kind})
	}
	if notifJSON {
		if err := printJSON(env.stdout, c, true); err != nil {
			return err
		}
		if c.SigningSecret != nil {
			fmt.Fprintln(env.stderr, notifSecretNote)
		}
		return nil
	}
	fmt.Fprintf(env.stdout, "notification channel %s added: %s, %s, %s\n", runs.SanitizeLine(c.Name), runs.SanitizeLine(c.Kind),
		runs.SanitizeLine(c.URLHint), eventsSummary(c.Subscriptions))
	if c.SigningSecret != nil {
		fmt.Fprintf(env.stdout, "signing secret:  %s\n", runs.SanitizeLine(*c.SigningSecret))
		fmt.Fprintln(env.stderr, notifSecretNote)
	}
	fmt.Fprintf(env.stderr, "send a test: nan notifications test %s\n", shellArg(c.Name))
	return nil
}

func doNotifEdit(ctx context.Context, env *runsEnv, ref string, p api.NotificationChannelPatch) error {
	existing, err := resolveChannel(ctx, env, ref, true)
	if err != nil {
		return err
	}
	c, err := env.client.UpdateNotificationChannel(ctx, existing.ID, p)
	if err != nil {
		return notifExit(err, env.cred, notifOp{scope: scopeNotificationsWrite, channel: existing.Name, kind: existing.Kind})
	}
	if notifJSON {
		return printJSON(env.stdout, c, true)
	}
	fmt.Fprintf(env.stdout, "notification channel %s updated: %s\n", runs.SanitizeLine(c.Name), channelStatus(c))
	if h := disabledHelp(c); h != "" {
		fmt.Fprintln(env.stderr, h)
	}
	return nil
}

func doNotifRm(ctx context.Context, env *runsEnv, ref string) error {
	existing, err := resolveChannel(ctx, env, ref, true)
	if err != nil {
		return err
	}
	name := runs.SanitizeLine(existing.Name)
	if err := confirm(env, notifRmYes, "delete notification channel "+name+"? It stops receiving notifications at once"); err != nil {
		return err
	}
	if err := env.client.DeleteNotificationChannel(ctx, existing.ID); err != nil {
		return notifExit(err, env.cred, notifOp{scope: scopeNotificationsWrite, channel: existing.Name})
	}
	fmt.Fprintf(env.stdout, "notification channel %s deleted\n", name)
	return nil
}

// testFailureHint is what to do about a test that was not delivered, by
// the platform's error class.
func testFailureHint(code, name string) string {
	switch code {
	case "timeout", "connect":
		return "the receiver did not answer in time; check it is up and try again"
	case "tls":
		return "the TLS handshake failed: the receiver needs a valid certificate for its name"
	case "dns":
		return "the URL's host does not resolve"
	case "blocked_address", "invalid_url":
		return "the URL's host resolves to an address NaN does not send to (private or NaN's own); use a public endpoint"
	case "redirect":
		return "the URL answered with a redirect, which NaN does not follow: use the final URL"
	case "http_4xx":
		return "the provider refused it: the webhook may have been deleted or revoked; replace the URL with: nan notifications edit " +
			shellArg(name) + " --url-file PATH"
	case "http_5xx":
		return "the receiver answered with a server error; try again later"
	case "rate_limited":
		return "this channel's delivery budget is spent for now (30 a minute, 1000 a day); try again later"
	case "channel_disabled":
		return "the channel is disabled: turn it on with: nan notifications edit " + shellArg(name) + " --enable"
	}
	return "see the delivery log: nan notifications deliveries " + shellArg(name)
}

func doNotifTest(ctx context.Context, env *runsEnv, ref string) error {
	existing, err := resolveChannel(ctx, env, ref, true)
	if err != nil {
		return err
	}
	res, err := env.client.TestNotificationChannel(ctx, existing.ID)
	if err != nil {
		return notifExit(err, env.cred, notifOp{scope: scopeNotificationsWrite, channel: existing.Name, test: true})
	}
	httpStatus := ""
	if res.HTTPStatus != nil {
		httpStatus = fmt.Sprintf(" (HTTP %d)", *res.HTTPStatus)
	}
	if notifJSON {
		if err := printJSON(env.stdout, res, true); err != nil {
			return err
		}
	}
	name := runs.SanitizeLine(existing.Name)
	if res.Delivered {
		if !notifJSON {
			fmt.Fprintf(env.stdout, "test notification delivered to %s%s\n", name, httpStatus)
		}
		return nil
	}
	code := ""
	if res.Error != nil {
		code = runs.SanitizeLine(*res.Error)
	}
	reason := runs.SanitizeLine(res.Status)
	if code != "" {
		reason += ", " + code
	}
	return exitf(exitFailed, "the test notification was not delivered to %s: %s%s. %s", name, reason, httpStatus, testFailureHint(code, existing.Name))
}

func doNotifRotate(ctx context.Context, env *runsEnv, ref string) error {
	existing, err := resolveChannel(ctx, env, ref, true)
	if err != nil {
		return err
	}
	if existing.Kind != "" && existing.Kind != api.NotificationKindHTTPS {
		return usageErrorf("only https channels have a signing secret: %s is a %s channel", runs.SanitizeLine(existing.Name),
			runs.SanitizeLine(existing.Kind))
	}
	out, err := env.client.RotateNotificationSigningSecret(ctx, existing.ID)
	if err != nil {
		return notifExit(err, env.cred, notifOp{scope: scopeNotificationsWrite, channel: existing.Name})
	}
	if notifJSON {
		if err := printJSON(env.stdout, out, true); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(env.stdout, "signing secret:  %s\n", runs.SanitizeLine(out.SigningSecret))
	}
	fmt.Fprintln(env.stderr, notifSecretNote)
	if out.PreviousExpiresAt != nil {
		fmt.Fprintf(env.stderr, "The previous secret keeps working until %s: requests carry both signatures until then.\n",
			localTime(*out.PreviousExpiresAt))
	}
	return nil
}

func doNotifDeliveries(ctx context.Context, env *runsEnv, ref string) error {
	existing, err := resolveChannel(ctx, env, ref, false)
	if err != nil {
		return err
	}
	list, err := env.client.NotificationDeliveries(ctx, existing.ID, notifDeliveriesLimit)
	if err != nil {
		return notifExit(err, env.cred, notifOp{scope: scopeNotificationsRead, channel: existing.Name})
	}
	if notifJSON {
		return printJSON(env.stdout, list, true)
	}
	if len(list.Data) == 0 {
		fmt.Fprintf(env.stderr, "No deliveries in the last 30 days. Send a test with: nan notifications test %s\n", shellArg(existing.Name))
		return nil
	}
	tw := tabwriter.NewWriter(env.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "WHEN\tEVENT\tSTATUS\tATTEMPT\tHTTP\tERROR\tRUN")
	for _, d := range list.Data {
		event := runs.SanitizeLine(d.Event)
		if d.Event == "digest" {
			event += fmt.Sprintf(" (%d)", d.Items)
		}
		httpStatus := "-"
		if d.HTTPStatus != nil {
			httpStatus = fmt.Sprintf("%d", *d.HTTPStatus)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%s\t%s\n", localTime(d.CreatedAt), event, runs.SanitizeLine(d.Status), d.Attempt,
			httpStatus, orDash(d.Error, 24), orDash(d.RunID, 40))
	}
	return tw.Flush()
}

func doNotifSubs(ctx context.Context, env *runsEnv, ref string, change bool) error {
	existing, err := resolveChannel(ctx, env, ref, false)
	if err != nil {
		return err
	}
	if !change {
		if notifJSON {
			return printJSON(env.stdout, struct {
				Subscriptions []api.NotificationSubscription `json:"subscriptions"`
			}{nonNilSubs(existing.Subscriptions)}, true)
		}
		printSubscriptions(env, existing.Subscriptions, automationNames(ctx, env, existing.Subscriptions))
		return nil
	}
	resolved := map[string]string{}
	parse := func(arg string) (api.NotificationSubscription, error) {
		event, auto, _ := parseSubscriptionArg(arg)
		s := api.NotificationSubscription{Event: event}
		if auto == "" {
			return s, nil
		}
		id, ok := resolved[auto]
		if !ok {
			var err error
			if id, err = resolveAutomation(ctx, env, auto); err != nil {
				return s, err
			}
			resolved[auto] = id
		}
		// The platform answers ids in lower case.
		id = strings.ToLower(id)
		s.AutomationID = &id
		return s, nil
	}
	same := func(a, b api.NotificationSubscription) bool {
		return a.Event == b.Event && ((a.AutomationID == nil && b.AutomationID == nil) ||
			(a.AutomationID != nil && b.AutomationID != nil && *a.AutomationID == *b.AutomationID))
	}
	var subs []api.NotificationSubscription
	switch {
	case notifSubsClear:
		subs = []api.NotificationSubscription{}
	case len(notifSubsSet) > 0:
		for _, a := range notifSubsSet {
			s, err := parse(a)
			if err != nil {
				return err
			}
			if !slices.ContainsFunc(subs, func(x api.NotificationSubscription) bool { return same(x, s) }) {
				subs = append(subs, s)
			}
		}
	default:
		subs = append([]api.NotificationSubscription{}, existing.Subscriptions...)
		for _, a := range notifSubsAdd {
			s, err := parse(a)
			if err != nil {
				return err
			}
			if !slices.ContainsFunc(subs, func(x api.NotificationSubscription) bool { return same(x, s) }) {
				subs = append(subs, s)
			}
		}
		for _, a := range notifSubsRemove {
			s, err := parse(a)
			if err != nil {
				return err
			}
			i := slices.IndexFunc(subs, func(x api.NotificationSubscription) bool { return same(x, s) })
			if i < 0 {
				return usageErrorf("%s is not subscribed to %s; see: nan notifications subscriptions %s",
					runs.SanitizeLine(existing.Name), runs.Truncate(runs.SanitizeLine(a), 80), shellArg(existing.Name))
			}
			subs = slices.Delete(subs, i, i+1)
		}
	}
	c, err := env.client.PutNotificationSubscriptions(ctx, existing.ID, subs)
	if err != nil {
		return notifExit(err, env.cred, notifOp{scope: scopeNotificationsWrite, channel: existing.Name})
	}
	if notifJSON {
		return printJSON(env.stdout, c, true)
	}
	printSubscriptions(env, c.Subscriptions, automationNames(ctx, env, c.Subscriptions))
	return nil
}

func nonNilSubs(s []api.NotificationSubscription) []api.NotificationSubscription {
	if s == nil {
		return []api.NotificationSubscription{}
	}
	return s
}
