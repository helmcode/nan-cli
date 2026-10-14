package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/nxssie/nan-cli/internal/api"
	"github.com/nxssie/nan-cli/internal/runs"
	"github.com/spf13/cobra"
)

// Automation writes from the CLI (write API): create, edit, delete,
// enable/disable, and the triggers of an automation. Bodies are the
// member's own YAML or JSON file, sent as written: the platform validates
// every field, exactly as for the portal.

var (
	autoWriteFile        string
	autoWriteJSON        bool
	autoRmYes            bool
	trigLsJSON           bool
	trigRmYes            bool
	trigRotateURL        bool
	trigRotateSec        bool
	trigRotateHard       bool
	trigRotateWhsecFile  string
	trigRotateWhsecStdin bool
)

const automationFileHelp = `The file is YAML or JSON (- reads it from stdin) with the fields of the
API's automation object, for example:

  name: PR review api
  workspace: my-workspace-id
  agent: pi
  model: glm5.3
  template: pr_review
  github_repo: acme/api
  instructions: Review the pull request for correctness.
  secrets:
    - secret: GITHUB_TOKEN
      env: GITHUB_TOKEN
      exposure: actions
  approval_kinds: [github_pr_review]

The full list of fields is in the API reference: https://nan.builders/docs/runs`

var automationsCreateCmd = &cobra.Command{
	Use:   "create -f FILE",
	Short: "Create an automation from a YAML or JSON file",
	Long: `Create an automation from a YAML or JSON file.

` + automationFileHelp + `

An automation receives the secrets you assign it, and approval_kinds: []
lets every action it proposes apply without asking you. Then add a trigger
with nan automations triggers add.

` + writeCredentialHelp + `

Docs: ` + writeDocs,
	Example: `  nan automations create -f pr-review.yaml
  nan automations create -f - < pr-review.json`,
	Args:        cobra.NoArgs,
	Annotations: sysexits,
	RunE: func(cmd *cobra.Command, args []string) error {
		body, err := readBodyFile(autoWriteFile)
		if err != nil {
			return err
		}
		return withWriteEnv(func(env *runsEnv) error { return doAutomationsCreate(cmd.Context(), env, body) })
	},
}

var automationsEditCmd = &cobra.Command{
	Use:   "edit <id|name> -f FILE",
	Short: "Change an automation: only the fields in the file change",
	Long: `Change an automation. The file (YAML or JSON, - for stdin) holds only the
fields to change; the others keep their value. For example, to stop asking
before pull request reviews are posted:

  approval_kinds: []

` + writeCredentialHelp + `

Docs: ` + writeDocs,
	Args:        oneAutomation,
	Annotations: sysexits,
	RunE: func(cmd *cobra.Command, args []string) error {
		body, err := readBodyFile(autoWriteFile)
		if err != nil {
			return err
		}
		return withWriteEnv(func(env *runsEnv) error { return doAutomationsUpdate(cmd.Context(), env, args[0], body, "updated") })
	},
}

var automationsEnableCmd = &cobra.Command{
	Use:         "enable <id|name>",
	Short:       "Enable an automation: its triggers start runs again",
	Args:        oneAutomation,
	Annotations: sysexits,
	RunE: func(cmd *cobra.Command, args []string) error {
		return withWriteEnv(func(env *runsEnv) error {
			return doAutomationsUpdate(cmd.Context(), env, args[0], json.RawMessage(`{"enabled":true}`), "enabled")
		})
	},
}

var automationsDisableCmd = &cobra.Command{
	Use:         "disable <id|name>",
	Short:       "Disable an automation: its triggers stop starting runs",
	Args:        oneAutomation,
	Annotations: sysexits,
	RunE: func(cmd *cobra.Command, args []string) error {
		return withWriteEnv(func(env *runsEnv) error {
			return doAutomationsUpdate(cmd.Context(), env, args[0], json.RawMessage(`{"enabled":false}`), "disabled")
		})
	},
}

var automationsRmCmd = &cobra.Command{
	Use:     "rm <id|name> [--yes]",
	Aliases: []string{"delete", "remove"},
	Short:   "Delete an automation and its triggers",
	Long: `Delete an automation and its triggers: its webhook URLs stop working at once.
Its past runs stay in nan runs ls.

Asks before deleting on a terminal; elsewhere, confirm with --yes.

` + writeCredentialHelp + `

Docs: ` + writeDocs,
	Args:        oneAutomation,
	Annotations: sysexits,
	RunE: func(cmd *cobra.Command, args []string) error {
		return withWriteEnv(func(env *runsEnv) error { return doAutomationsRm(cmd.Context(), env, args[0]) })
	},
}

var signingSecretFlags = secretInputFlags{"--signing-secret-file", "--signing-secret-stdin", "pbpaste | nan automations triggers rotate NAME ID --signing-secret-stdin"}

const oneTimeNote = "Store these now: they are never shown again. Lost them? Issue new ones with: nan automations triggers rotate"

var automationsTriggersCmd = &cobra.Command{
	Use:     "triggers",
	Aliases: []string{"trigger"},
	Short:   "List, add, change, rotate and delete an automation's triggers",
	Long: `List, add, change, rotate and delete the triggers of an automation: webhooks
and schedules.

A webhook's URL and signing secret are shown once, when the trigger is added
or rotated, and never again: store them in the sender (GitHub, your CI)
right away. They go to stdout only, never to a file this CLI writes.

` + writeCredentialHelp + `

Docs: https://nan.builders/docs/webhooks`,
	Annotations: sysexits,
	Args:        cobra.ArbitraryArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 {
			return usageErrorf("unknown command %q for \"nan automations triggers\"; try: nan automations triggers --help", runs.Truncate(runs.SanitizeLine(args[0]), 64))
		}
		return cmd.Help()
	},
}

var triggersLsCmd = &cobra.Command{
	Use:         "ls <automation id|name>",
	Short:       "List an automation's triggers",
	Args:        oneAutomation,
	Annotations: sysexits,
	RunE: func(cmd *cobra.Command, args []string) error {
		return withWriteEnv(func(env *runsEnv) error { return doTriggersLs(cmd.Context(), env, args[0]) })
	},
}

var triggersAddCmd = &cobra.Command{
	Use:   "add <automation id|name> -f FILE",
	Short: "Add a webhook or a schedule; a webhook's URL and secret are shown once",
	Long: `Add a trigger from a YAML or JSON file (- for stdin). A GitHub webhook:

  kind: webhook
  signature_scheme: github
  event_filter:
    events: [pull_request]

A schedule:

  kind: schedule
  cron: "0 7 * * 1-5"
  timezone: Europe/Madrid

For a webhook, the URL and the signing secret are printed once, to stdout.
` + oneTimeNote + `.
Adding is not retried: if the answer is lost, list the triggers and rotate
or delete the one you do not have the secret of.

Docs: https://nan.builders/docs/webhooks`,
	Args:        oneAutomation,
	Annotations: sysexits,
	RunE: func(cmd *cobra.Command, args []string) error {
		body, err := readBodyFile(autoWriteFile)
		if err != nil {
			return err
		}
		return withWriteEnv(func(env *runsEnv) error { return doTriggersAdd(cmd.Context(), env, args[0], body) })
	},
}

var triggersEditCmd = &cobra.Command{
	Use:   "edit <automation id|name> <trigger id> -f FILE",
	Short: "Change a trigger: only the fields in the file change",
	Long: `Change a trigger. The file (YAML or JSON, - for stdin) holds only the fields
to change, for example:

  enabled: false

Docs: https://nan.builders/docs/webhooks`,
	Args:        automationAndTrigger,
	Annotations: sysexits,
	RunE: func(cmd *cobra.Command, args []string) error {
		body, err := readBodyFile(autoWriteFile)
		if err != nil {
			return err
		}
		return withWriteEnv(func(env *runsEnv) error { return doTriggersEdit(cmd.Context(), env, args[0], args[1], body) })
	},
}

var triggersRotateCmd = &cobra.Command{
	Use:   "rotate <automation id|name> <trigger id> [--url] [--secret] [--no-grace] [--signing-secret-file PATH | --signing-secret-stdin]",
	Short: "Issue a new webhook URL and/or signing secret, shown once",
	Long: `Issue a new webhook URL, a new signing secret, or both (the default). The new
values are printed once, to stdout. ` + oneTimeNote + `.

With --secret, the previous signing secret keeps working for 24 hours so
the sender can be updated; --no-grace ends it at once (for a leaked secret).
A new URL replaces the old one at once.

A Stripe webhook's signing secret is issued by Stripe, not by NaN: roll it
in the Stripe dashboard, then hand the new whsec_ over with
--signing-secret-file PATH or --signing-secret-stdin (a pipe, never typed
or passed as an argument). It replaces the stored one and is not printed.

Docs: https://nan.builders/docs/webhooks`,
	Args:        automationAndTrigger,
	Annotations: sysexits,
	RunE: func(cmd *cobra.Command, args []string) error {
		in := api.RotateRequest{Grace: !trigRotateHard}
		rotateSecret := trigRotateSec || trigRotateWhsecFile != "" || trigRotateWhsecStdin
		if trigRotateURL || !rotateSecret {
			in.Rotate = append(in.Rotate, "url")
		}
		if rotateSecret || !trigRotateURL {
			in.Rotate = append(in.Rotate, "secret")
		}
		if trigRotateWhsecFile != "" || trigRotateWhsecStdin {
			if trigRotateWhsecFile != "" && trigRotateWhsecStdin {
				return usageErrorf("give --signing-secret-file or --signing-secret-stdin, not both")
			}
			whsec, err := readSecretInput(signingSecretFlags, trigRotateWhsecFile, trigRotateWhsecStdin)
			if err != nil {
				return err
			}
			in.SigningSecret = &whsec
		}
		return withWriteEnv(func(env *runsEnv) error { return doTriggersRotate(cmd.Context(), env, args[0], args[1], in) })
	},
}

var triggersRmCmd = &cobra.Command{
	Use:         "rm <automation id|name> <trigger id> [--yes]",
	Aliases:     []string{"delete", "remove"},
	Short:       "Delete a trigger: its webhook URL stops working at once",
	Args:        automationAndTrigger,
	Annotations: sysexits,
	RunE: func(cmd *cobra.Command, args []string) error {
		return withWriteEnv(func(env *runsEnv) error { return doTriggersRm(cmd.Context(), env, args[0], args[1]) })
	},
}

func init() {
	automationsCmd.AddCommand(automationsCreateCmd, automationsEditCmd, automationsEnableCmd, automationsDisableCmd,
		automationsRmCmd, automationsTriggersCmd)
	automationsTriggersCmd.AddCommand(triggersLsCmd, triggersAddCmd, triggersEditCmd, triggersRotateCmd, triggersRmCmd)

	for _, c := range []*cobra.Command{automationsCreateCmd, automationsEditCmd, triggersAddCmd, triggersEditCmd} {
		c.Flags().StringVarP(&autoWriteFile, "file", "f", "", "YAML or JSON `FILE` (- for stdin)")
	}
	for _, c := range []*cobra.Command{automationsCreateCmd, automationsEditCmd, automationsEnableCmd, automationsDisableCmd,
		triggersAddCmd, triggersEditCmd, triggersRotateCmd} {
		c.Flags().BoolVar(&autoWriteJSON, "json", false, "Print the API's answer as JSON")
	}
	automationsRmCmd.Flags().BoolVar(&autoRmYes, "yes", false, "Delete without asking")
	triggersRmCmd.Flags().BoolVar(&trigRmYes, "yes", false, "Delete without asking")
	triggersLsCmd.Flags().BoolVar(&trigLsJSON, "json", false, "Print the triggers as JSON")
	triggersRotateCmd.Flags().BoolVar(&trigRotateURL, "url", false, "Rotate the webhook URL")
	triggersRotateCmd.Flags().BoolVar(&trigRotateSec, "secret", false, "Rotate the signing secret")
	triggersRotateCmd.Flags().BoolVar(&trigRotateHard, "no-grace", false, "End the previous signing secret at once (a leaked secret)")
	triggersRotateCmd.Flags().StringVar(&trigRotateWhsecFile, "signing-secret-file", "", "Stripe only: read the new whsec_ from this file")
	triggersRotateCmd.Flags().BoolVar(&trigRotateWhsecStdin, "signing-secret-stdin", false, "Stripe only: read the new whsec_ from a pipe")
}

func automationAndTrigger(cmd *cobra.Command, args []string) error {
	switch {
	case len(args) < 2:
		return usageErrorf("give the automation (id or name) and the trigger id; see: nan automations triggers ls <automation>")
	case len(args) > 2:
		return usageErrorf("one automation and one trigger id (quote a name with spaces)")
	case !runIDPattern.MatchString(args[1]):
		return usageErrorf("%q is not a trigger id; see: nan automations triggers ls <automation>", runs.Truncate(runs.SanitizeLine(args[1]), 64))
	}
	return oneAutomation(cmd, args[:1])
}

// automationWriteExit is writeExit with a 404 that names the automation.
func automationWriteExit(err error, ref string, cred credential) *ExitError {
	var apiErr *api.APIError
	if errors.As(err, &apiErr) && apiErr.Status == 404 {
		return automationExit(err, ref, cred)
	}
	return writeExit(err, cred, "automations", scopeAutomationsWrite)
}

// resolveForWrite is resolveAutomation after checking the write API is on.
func resolveForWrite(ctx context.Context, env *runsEnv, ref string) (string, error) {
	if err := requireWriteAPI(ctx, env, "automations"); err != nil {
		return "", err
	}
	return resolveAutomation(ctx, env, ref)
}

func doAutomationsCreate(ctx context.Context, env *runsEnv, body json.RawMessage) error {
	if err := requireWriteAPI(ctx, env, "automations"); err != nil {
		return err
	}
	a, raw, err := env.client.CreateAutomation(ctx, body)
	if err != nil {
		return writeExit(err, env.cred, "automations", scopeAutomationsWrite)
	}
	if autoWriteJSON {
		return printJSON(env.stdout, raw, true)
	}
	fmt.Fprintf(env.stdout, "automation %s created: %s\n", runs.SanitizeLine(a.Name), runs.SanitizeLine(a.ID))
	warnAutomatic(env, a)
	fmt.Fprintf(env.stderr, "add a trigger: nan automations triggers add %s -f trigger.yaml\nrun it now: nan automations run %s\n",
		runs.SanitizeLine(a.ID), runs.SanitizeLine(a.ID))
	return nil
}

// warnAutomatic says when nothing an automation proposes waits for the
// member: pushes and pull requests apply on their own.
func warnAutomatic(env *runsEnv, a *api.Automation) {
	if len(a.ApprovalKinds) == 0 {
		fmt.Fprintln(env.stderr, "note: approval_kinds is empty, so every action its runs propose applies without asking you")
	}
}

func doAutomationsUpdate(ctx context.Context, env *runsEnv, ref string, body json.RawMessage, verb string) error {
	id, err := resolveForWrite(ctx, env, ref)
	if err != nil {
		return err
	}
	a, raw, err := env.client.UpdateAutomation(ctx, id, body)
	if err != nil {
		return automationWriteExit(err, ref, env.cred)
	}
	if autoWriteJSON {
		return printJSON(env.stdout, raw, true)
	}
	fmt.Fprintf(env.stdout, "automation %s %s (%s)\n", runs.SanitizeLine(a.Name), verb, automationStatus(*a))
	if verb == "updated" {
		warnAutomatic(env, a)
	}
	return nil
}

func doAutomationsRm(ctx context.Context, env *runsEnv, ref string) error {
	id, err := resolveForWrite(ctx, env, ref)
	if err != nil {
		return err
	}
	shown := runs.Truncate(runs.SanitizeLine(ref), 64)
	if err := confirm(env, autoRmYes, "delete automation "+shown+" and its triggers?"); err != nil {
		return err
	}
	if err := env.client.DeleteAutomation(ctx, id); err != nil {
		return automationWriteExit(err, ref, env.cred)
	}
	fmt.Fprintf(env.stdout, "automation %s deleted\n", shown)
	return nil
}

func doTriggersLs(ctx context.Context, env *runsEnv, ref string) error {
	id, err := resolveAutomation(ctx, env, ref)
	if err != nil {
		return err
	}
	list, err := env.client.ListTriggers(ctx, id)
	if err != nil {
		return automationExit(err, ref, env.cred)
	}
	if trigLsJSON {
		return printJSON(env.stdout, list, true)
	}
	if len(list.Data) == 0 {
		fmt.Fprintf(env.stderr, "No triggers. Add one with: nan automations triggers add %s -f trigger.yaml\n", runs.SanitizeLine(id))
		return nil
	}
	tw := tabwriter.NewWriter(env.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tTRIGGER\tCHANGED FROM")
	for _, t := range list.Data {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", runs.SanitizeLine(t.ID), triggerLine(t), dashIfEmpty(updatedByLabel(t.UpdatedBy)))
	}
	return tw.Flush()
}

func doTriggersAdd(ctx context.Context, env *runsEnv, ref string, body json.RawMessage) error {
	id, err := resolveForWrite(ctx, env, ref)
	if err != nil {
		return err
	}
	out, err := env.client.CreateTrigger(ctx, id, body)
	if err != nil {
		return automationWriteExit(err, ref, env.cred)
	}
	if autoWriteJSON {
		if err := printJSON(env.stdout, out, true); err != nil {
			return err
		}
		if out.WebhookURL != nil || out.SigningSecret != nil {
			fmt.Fprintln(env.stderr, oneTimeNote)
		}
		return nil
	}
	if out.Trigger != nil {
		fmt.Fprintf(env.stderr, "trigger %s added: %s\n", runs.SanitizeLine(out.Trigger.ID), triggerLine(*out.Trigger))
	}
	printTriggerSecrets(env, out)
	return nil
}

// printTriggerSecrets prints the one-time webhook URL and signing secret to
// stdout, and the note about them to stderr.
func printTriggerSecrets(env *runsEnv, s *api.TriggerSecrets) {
	if s.WebhookURL == nil && s.SigningSecret == nil {
		return
	}
	if s.WebhookURL != nil {
		fmt.Fprintf(env.stdout, "webhook URL:     %s\n", runs.SanitizeLine(*s.WebhookURL))
	}
	if s.SigningSecret != nil {
		fmt.Fprintf(env.stdout, "signing secret:  %s\n", runs.SanitizeLine(*s.SigningSecret))
	}
	fmt.Fprintln(env.stderr, oneTimeNote)
}

func doTriggersEdit(ctx context.Context, env *runsEnv, ref, triggerID string, body json.RawMessage) error {
	id, err := resolveForWrite(ctx, env, ref)
	if err != nil {
		return err
	}
	t, err := env.client.UpdateTrigger(ctx, id, triggerID, body)
	if err != nil {
		return triggerWriteExit(err, ref, triggerID, env.cred)
	}
	if autoWriteJSON {
		return printJSON(env.stdout, t, true)
	}
	fmt.Fprintf(env.stdout, "trigger %s updated: %s\n", runs.SanitizeLine(t.ID), triggerLine(*t))
	return nil
}

func doTriggersRotate(ctx context.Context, env *runsEnv, ref, triggerID string, in api.RotateRequest) error {
	id, err := resolveForWrite(ctx, env, ref)
	if err != nil {
		return err
	}
	out, err := env.client.RotateTrigger(ctx, id, triggerID, in)
	if err != nil {
		var apiErr *api.APIError
		if errors.As(err, &apiErr) && apiErr.Status == 400 && apiErr.Param == "signing_secret" {
			if in.SigningSecret == nil {
				return usageErrorf("this is a Stripe webhook: its signing secret comes from the Stripe dashboard. " +
					"Roll it there and pass the new whsec_ with --signing-secret-file PATH or --signing-secret-stdin, " +
					"or rotate only the URL with --url")
			}
			if strings.Contains(apiErr.Message, "Stripe") {
				return usageErrorf("the server refused the signing secret: %s. Copy the endpoint's whsec_ from the Stripe dashboard again",
					runs.Truncate(runs.SanitizeLine(apiErr.Message), 200))
			}
			return usageErrorf("the server refused the signing secret: %s. NaN generates the signing secret of GitHub and NaN webhooks; "+
				"--signing-secret-file and --signing-secret-stdin are for Stripe's whsec_", runs.Truncate(runs.SanitizeLine(apiErr.Message), 200))
		}
		return triggerWriteExit(err, ref, triggerID, env.cred)
	}
	if autoWriteJSON {
		if err := printJSON(env.stdout, out, true); err != nil {
			return err
		}
		if out.WebhookURL != nil || out.SigningSecret != nil {
			fmt.Fprintln(env.stderr, oneTimeNote)
		}
		return nil
	}
	printTriggerSecrets(env, out)
	if in.SigningSecret != nil {
		fmt.Fprintln(env.stderr, "the signing secret you gave (from Stripe) is now the one checked")
	}
	if out.SigningSecret != nil || in.SigningSecret != nil {
		if in.Grace {
			fmt.Fprintln(env.stderr, "the previous signing secret keeps working for 24 hours")
		} else {
			fmt.Fprintln(env.stderr, "the previous signing secret no longer works")
		}
	}
	return nil
}

func doTriggersRm(ctx context.Context, env *runsEnv, ref, triggerID string) error {
	id, err := resolveForWrite(ctx, env, ref)
	if err != nil {
		return err
	}
	if err := confirm(env, trigRmYes, "delete trigger "+triggerID+"? A webhook URL stops working at once"); err != nil {
		return err
	}
	if err := env.client.DeleteTrigger(ctx, id, triggerID); err != nil {
		return triggerWriteExit(err, ref, triggerID, env.cred)
	}
	fmt.Fprintf(env.stdout, "trigger %s deleted\n", triggerID)
	return nil
}

// triggerWriteExit names the trigger on a 404: the automation was found a
// moment ago, so it is the trigger that is missing.
func triggerWriteExit(err error, ref, triggerID string, cred credential) *ExitError {
	var apiErr *api.APIError
	if errors.As(err, &apiErr) && apiErr.Status == 404 {
		return usageErrorf("trigger %s not found on automation %q; see: nan automations triggers ls %q",
			triggerID, runs.Truncate(runs.SanitizeLine(ref), 64), runs.Truncate(runs.SanitizeLine(ref), 64))
	}
	return writeExit(err, cred, "automations", scopeAutomationsWrite)
}
