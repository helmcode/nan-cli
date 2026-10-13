package cli

import (
	"context"
	"fmt"
	"strings"

	"charm.land/huh/v2"
	"github.com/spf13/cobra"

	"github.com/nxssie/nan-cli/internal/api"
	"github.com/nxssie/nan-cli/internal/build"
	"github.com/nxssie/nan-cli/internal/session"
	"github.com/nxssie/nan-cli/internal/tools"
	"github.com/nxssie/nan-cli/internal/ui"
)

func newSetupCmd(get func() *app) *cobra.Command {
	var apiKey string
	var all bool

	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Point the coding tools on this machine at NaN",
		Long: "Point the coding tools on this machine at NaN: " + tools.List() + ".\n\n" +
			"Paste your API key - from cloud.nan.builders - and this writes it into the\n" +
			"tools you pick. Only installed tools are offered, only NaN's part of each\n" +
			"config is touched, and unticking a tool takes NaN back out of it.\n\n" +
			"The key is checked against the cluster before anything is written, so a\n" +
			"mistyped one is caught here rather than as a 401 inside every tool.\n\n" +
			"For a machine with nobody at the keyboard:\n\n" +
			"  echo \"$NAN_API_KEY\" | nan setup --key - --all\n",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSetup(cmd.Context(), get(), apiKey, all)
		},
	}
	cmd.Flags().StringVar(&apiKey, "key", "",
		`the API key to write ("-" reads it from stdin, keeping it out of your shell history)`)
	cmd.Flags().BoolVar(&all, "all", false, "configure every installed tool without asking")
	cmd.AddCommand(newSetupStatusCmd(get), newSetupRemoveCmd(get))
	return cmd
}

func runSetup(ctx context.Context, a *app, apiKey string, all bool) error {
	p := a.p

	// The key, from the flag, from what is already saved, or from a prompt.
	if apiKey != "" {
		var err error
		if apiKey, err = flagValue(apiKey); err != nil {
			return err
		}
	}
	if apiKey == "" {
		apiKey = a.session.APIKey
	}
	if apiKey == "" {
		if !p.Interactive() {
			return fmt.Errorf("no API key - pass one with --key (or \"--key -\" to read it from stdin)")
		}
		var err error
		if apiKey, err = askAPIKey(ctx); err != nil {
			return err
		}
	}

	// Checked before a byte is written. A key that fails here is one
	// message; the same key written into five configs is five tools that
	// start, answer, and fail deep into a session.
	models, err := api.ListModels(ctx, apiKey)
	if err != nil {
		return fmt.Errorf("the cluster refused this key - %w", err)
	}

	installed := tools.Installed(tools.Detect())
	if len(installed) == 0 {
		a.session.APIKey = apiKey
		if err := session.Save(a.session); err != nil {
			return err
		}
		p.Success("Key accepted by the cluster · %d models. Saved.", len(models))
		p.Note("None of %s is installed on this machine yet.", tools.List())
		return nil
	}

	chosen, err := pickTools(ctx, a, installed, all)
	if err != nil {
		return err
	}

	result := tools.Apply(apiKey, chosen)

	a.session.APIKey = apiKey
	a.session.EnabledTools = chosen
	if err := session.Save(a.session); err != nil {
		return err
	}

	p.Success("Key accepted by the cluster · %d models  ·  %s", len(models), result.Summary())
	if len(result.Written) > 0 {
		p.Section("now open them")
		steps := map[string]tools.Tool{}
		for _, t := range tools.Registry() {
			steps[t.Name] = t
		}
		rows := make([][2]string, 0, len(result.Written))
		for _, name := range result.Written {
			rows = append(rows, [2]string{ui.Mono(steps[name].Open), steps[name].NextStep})
		}
		p.KV(rows)
		p.Blank()
	}
	for _, f := range result.Failed {
		p.Warn("%s: %s", f.Tool, f.FirstLine())
	}
	if len(result.Failed) > 0 {
		p.Note("The rest went in. Its page on nan.builders has the manual steps.")
		return fmt.Errorf("%d tool(s) could not be configured", len(result.Failed))
	}
	return nil
}

func askAPIKey(ctx context.Context) (string, error) {
	var key string
	form := ui.NewForm(huh.NewGroup(
		huh.NewInput().
			Title("Your NaN API key").
			Description("Copy it from cloud.nan.builders. It is checked against the cluster before anything is written.").
			Placeholder("paste your NaN API key here").
			// Dots, not the key: a terminal is shared and screenshotted.
			EchoMode(huh.EchoModePassword).
			Value(&key).
			Validate(func(v string) error {
				if strings.TrimSpace(v) == "" {
					return fmt.Errorf("paste the key")
				}
				return nil
			}),
	))

	if err := form.RunWithContext(ctx); err != nil {
		if errorsIsUserAbort(err) {
			return "", fmt.Errorf("cancelled")
		}
		return "", err
	}
	return strings.TrimSpace(key), nil
}

// pickTools asks which tools to configure, or takes every installed one when
// there is nobody to ask and --all was passed.
func pickTools(ctx context.Context, a *app, installed []tools.Status, all bool) (map[string]bool, error) {
	if all || !a.p.Interactive() {
		if !all {
			return nil, fmt.Errorf("there is nobody here to ask which tools - pass --all to configure every installed one")
		}
		chosen := map[string]bool{}
		for _, st := range installed {
			chosen[st.Name] = true
		}
		return chosen, nil
	}

	options := make([]huh.Option[string], 0, len(installed))
	selected := make([]string, 0, len(installed))
	for _, st := range installed {
		label := st.Name
		if st.Configured {
			label += " · configured with NaN"
		}
		on := tools.Enabled(a.session.EnabledTools, st.Name)
		options = append(options, huh.NewOption(label, st.Name).Selected(on))
		if on {
			selected = append(selected, st.Name)
		}
	}

	// A Picker rather than a bare MultiSelect, which shows no options at all
	// under a description - see ui.Picker.
	form := ui.NewForm(huh.NewGroup(
		ui.NewPicker(
			"Which tools should use NaN?",
			"Only the tools installed here are listed. Unticking one takes NaN back out of its config.",
			options,
		).Value(&selected),
	))

	if err := form.RunWithContext(ctx); err != nil {
		if errorsIsUserAbort(err) {
			return nil, fmt.Errorf("cancelled")
		}
		return nil, err
	}

	// Every installed tool, ticked or not: one missing from the map counts as
	// ticked, so an untick has to be written down to stick.
	chosen := make(map[string]bool, len(installed))
	for _, st := range installed {
		chosen[st.Name] = false
	}
	for _, name := range selected {
		chosen[name] = true
	}
	return chosen, nil
}

func newSetupStatusCmd(get func() *app) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "What is configured on this machine",
		Long: "What is configured on this machine: whether there is an API key, and\n" +
			"which tools are pointed at NaN.\n\n" +
			"Reads nothing over the network, so it works with no connection and\n" +
			"without being signed in.\n",
		RunE: func(_ *cobra.Command, _ []string) error {
			a := get()
			detected := tools.Detect()

			if a.p.JSONMode() {
				out := make([]map[string]any, 0, len(detected))
				for _, st := range detected {
					out = append(out, map[string]any{
						"tool": st.Name, "installed": st.Installed,
						"configured": st.Configured, "config": st.ConfigPath,
					})
				}
				return a.p.JSON(map[string]any{"hasKey": a.session.APIKey != "", "tools": out})
			}

			a.p.Section("your api key")
			if a.session.APIKey == "" {
				a.p.Note("No key on this machine. Run: %s setup", build.Name)
			} else {
				a.p.KV([][2]string{
					{"Key", ui.Mono(ui.Fingerprint(a.session.APIKey))},
					{"Endpoint", ui.Mono(api.InferenceURL())},
				})
			}

			a.p.Section("tools on this machine")
			rows := make([][]string, 0, len(detected))
			for _, st := range detected {
				status := ui.Faint("not installed")
				switch {
				case st.Configured:
					status = ui.Tag("nan", ui.TagOK)
				case st.Installed:
					status = ui.Faint("not configured")
				}
				rows = append(rows, []string{ui.Value(st.Name), status, ui.Faint(ui.HomePath(st.ConfigPath))})
			}
			a.p.Table(ui.Table{Columns: []ui.Column{
				{Title: "tool", Max: 18}, {Title: "status"}, {Title: "config", Max: 50},
			}, Rows: rows}, "No supported tools found.")
			a.p.Blank()
			return nil
		},
	}
}

func newSetupRemoveCmd(get func() *app) *cobra.Command {
	return &cobra.Command{
		Use:   "remove",
		Short: "Take NaN back out of every tool",
		Long: "Take NaN back out of every tool, and forget the key here.\n\n" +
			"Only our section of each config is removed; everything else in those\n" +
			"files is left exactly as it was.\n",
		RunE: func(_ *cobra.Command, _ []string) error {
			a := get()
			removed, failed := tools.RemoveAll()

			a.session.APIKey = ""
			a.session.EnabledTools = nil
			if err := session.Save(a.session); err != nil {
				return err
			}

			if len(removed) > 0 {
				a.p.Success("Removed the key from %s.", strings.Join(removed, ", "))
			} else {
				a.p.Note("No tool on this machine was using it.")
			}
			for _, f := range failed {
				a.p.Warn("%s: %s", f.Tool, f.FirstLine())
			}
			if len(failed) > 0 {
				return fmt.Errorf("the key is still in %d tool(s)", len(failed))
			}
			return nil
		},
	}
}
