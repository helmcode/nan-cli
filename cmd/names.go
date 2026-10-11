package cmd

import (
	"context"
	"strings"
	"time"

	"github.com/nxssie/nan-cli/internal/api"
)

// The platform names a run's or an approval's workspace and automation by id
// only. People look for names, so the text views look the names up here,
// once per command, and show the id when that is not possible: a platform
// token without workspaces:read, a network error, an id that is not in the
// list. A name is a nicety; it never fails a command, and --json output
// never asks for one (it stays the API's own shape).

// nameLookupTimeout bounds each lookup, so a slow answer to a question the
// command does not need answered cannot hold up the one it does.
const nameLookupTimeout = 3 * time.Second

// nameCache holds the names a command looked up. The zero value is ready:
// each list is fetched the first time a name from it is asked for, and
// never again, whatever the answer was.
type nameCache struct {
	workspaces, automations           map[string]string
	workspacesTried, automationsTried bool
}

func lookupKey(id string) string { return strings.ToLower(strings.TrimSpace(id)) }

// workspaceName is the name of the member's workspace with this id, or ""
// when it cannot be known.
func (env *runsEnv) workspaceName(ctx context.Context, id string) string {
	if lookupKey(id) == "" {
		return ""
	}
	if !env.names.workspacesTried {
		env.names.workspacesTried = true
		lctx, cancel := context.WithTimeout(ctx, nameLookupTimeout)
		defer cancel()
		if list, err := env.client.ListWorkspaces(lctx); err == nil {
			env.names.workspaces = make(map[string]string, len(list))
			for _, w := range list {
				env.names.workspaces[lookupKey(w.ID)] = w.Name
			}
		}
	}
	return env.names.workspaces[lookupKey(id)]
}

// automationName is the name of the member's automation with this id, or ""
// when it cannot be known.
func (env *runsEnv) automationName(ctx context.Context, id string) string {
	if lookupKey(id) == "" {
		return ""
	}
	if !env.names.automationsTried {
		env.names.automationsTried = true
		lctx, cancel := context.WithTimeout(ctx, nameLookupTimeout)
		defer cancel()
		if list, err := env.client.ListAutomations(lctx); err == nil {
			env.names.automations = make(map[string]string, len(list.Data))
			for _, a := range list.Data {
				env.names.automations[lookupKey(a.ID)] = a.Name
			}
		}
	}
	return env.names.automations[lookupKey(id)]
}

// nameWorkspace fills in a workspace reference's name when the platform sent
// only its id.
func (env *runsEnv) nameWorkspace(ctx context.Context, r *api.Ref) {
	if r != nil && r.Name == "" {
		r.Name = env.workspaceName(ctx, r.ID)
	}
}

// nameAutomation fills in an automation reference's name when the platform
// sent only its id.
func (env *runsEnv) nameAutomation(ctx context.Context, r *api.Ref) {
	if r != nil && r.Name == "" {
		r.Name = env.automationName(ctx, r.ID)
	}
}

// nameRunWorkspace fills in a run's workspace name when it has none (the
// platform sends it, except for a workspace deleted since).
func (env *runsEnv) nameRunWorkspace(ctx context.Context, run *api.Run) {
	if run.Workspace.Name == "" {
		run.Workspace.Name = env.workspaceName(ctx, run.Workspace.ID)
	}
}

// nameRun fills in the names a run's text views show.
func (env *runsEnv) nameRun(ctx context.Context, run *api.Run) {
	env.nameRunWorkspace(ctx, run)
	env.nameAutomation(ctx, run.Automation)
}

// refLabel is a name for a table: the name when it is known, else the id.
func refLabel(name, id string) string {
	if name != "" {
		return name
	}
	return id
}
