package pulse

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"

	"github.com/nxssie/nan-cli/internal/api"
	catalog "github.com/nxssie/nan-cli/internal/models"
	"github.com/nxssie/nan-cli/internal/tools"
)

// snapshot is everything the dashboard knows, fetched together.
//
// One load, not one per area: Home is built from all of it at once - the
// session, the key, the tools, the usage - so a page that loaded piecemeal
// would spend its first seconds saying nothing needs you, and then change
// its mind.
type snapshot struct {
	me        *api.Me
	usage     *api.Usage
	keyStatus *api.KeyStatus
	models    []modelInfo
	// tools is every supported tool and its state on this machine, read
	// here rather than on every frame: it means reading their configs.
	tools []tools.Status

	// release is the newest CLI release, when it was asked for this load;
	// askedRelease says whether it was, since "no answer" is also one.
	release      *api.Release
	askedRelease bool

	// token is the session this load was made with, so a refusal that
	// arrives after a newer sign-in does not end the newer session.
	token string
	// expired is the platform refusing that session.
	expired bool
	// keyRefused is the cluster refusing the API key on this machine.
	keyRefused bool

	// err is the first thing that failed. Everything that did arrive is
	// still shown: a dashboard with no usage is more use than none.
	err error
}

// modelInfo is one model as Models lists it: its id and what kind it is.
type modelInfo struct {
	id   string
	kind string
}

type loadedMsg struct{ snap snapshot }

// load fetches the snapshot, every call at once. With no session it fetches
// what a key alone can: the models.
func load(ctx context.Context, c *api.Client, apiKey string, askRelease bool) snapshot {
	var (
		s    = snapshot{token: c.Token()}
		mu   sync.Mutex
		wg   sync.WaitGroup
		keep = func(err error) {
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
			case errors.Is(err, api.ErrSessionExpired):
				s.expired = true
			case s.err == nil:
				s.err = err
			}
		}
	)
	run := func(f func()) {
		wg.Add(1)
		go func() { defer wg.Done(); f() }()
	}
	if c.Token() != "" {
		run(func() { me, err := c.Me(ctx); keep(err); s.me = me })
		run(func() { u, err := c.Usage(ctx); keep(err); s.usage = u })
		// The key's status only says where to find a key, so a failure to
		// read it is not worth saying.
		run(func() { k, _ := c.KeyStatus(ctx); s.keyStatus = k })
	}
	run(func() {
		models, refused, err := loadModels(ctx, c, apiKey)
		keep(err)
		s.models, s.keyRefused = models, refused
	})
	run(func() { s.tools = tools.Detect() })
	if askRelease {
		s.askedRelease = true
		run(func() { r, _ := api.LatestRelease(ctx); s.release = r })
	}
	wg.Wait()
	return s
}

// loadModels asks the cluster which models the key can call - the list the
// docs call definitive - and falls back on the platform's own list when
// there is no key to ask with.
func loadModels(ctx context.Context, c *api.Client, apiKey string) ([]modelInfo, bool, error) {
	if apiKey != "" {
		ids, err := api.ListModels(ctx, apiKey)
		if errors.Is(err, api.ErrKeyRefused) {
			return nil, true, err
		}
		if err != nil {
			return nil, false, err
		}
		out := make([]modelInfo, 0, len(ids))
		for _, id := range ids {
			out = append(out, modelInfo{id: id, kind: kindOf(id)})
		}
		return out, false, nil
	}
	if c.Token() == "" {
		return nil, false, nil
	}
	list, err := c.AgentModels(ctx)
	if err != nil {
		return nil, false, err
	}
	out := make([]modelInfo, 0, len(list))
	for _, m := range list {
		kind := modeLabel[m.Mode]
		if kind == "" {
			kind = strings.ReplaceAll(m.Mode, "_", " ")
		}
		out = append(out, modelInfo{id: m.Name, kind: kind})
	}
	return out, false, nil
}

// kindOf is what the catalogue says an id is, or "unknown": worth showing
// rather than hiding, because that is how a model nobody documented gets
// noticed.
func kindOf(id string) string {
	m, ok := catalog.Get(id)
	if !ok {
		return "unknown"
	}
	if m.Premium {
		return string(m.Kind) + " · premium"
	}
	return string(m.Kind)
}

// The platform's modes, in words.
var modeLabel = map[string]string{
	"":                    "text generation",
	"audio_speech":        "audio speech",
	"audio_transcription": "audio transcription",
	"embedding":           "embedding",
}

// ── derived views of the snapshot ────────────────────────────────────────

// periods are the usage windows, in the order they are shown.
func (s snapshot) periods() []namedPeriod {
	if s.usage == nil {
		return nil
	}
	return []namedPeriod{
		{"Last 24 hours", "24h", s.usage.Last24h},
		{"Last 30 days", "30 days", s.usage.Last30d},
		{"All time", "all time", s.usage.AllTime},
	}
}

type namedPeriod struct {
	title string
	short string
	api.Period
}

// tokens is one model's total in a period.
func tokensIn(p api.Period, model string) float64 {
	for _, m := range p.ByModel {
		if m.Model == model {
			return m.Total()
		}
	}
	return 0
}

// usedModels is every model with usage in any period, biggest over thirty
// days first, then by name.
func (s snapshot) usedModels() []string {
	if s.usage == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, p := range []api.Period{s.usage.Last30d, s.usage.Last24h, s.usage.AllTime} {
		for _, m := range p.ByModel {
			if m.Model != "" && !seen[m.Model] {
				seen[m.Model] = true
				out = append(out, m.Model)
			}
		}
	}
	sort.SliceStable(out, func(a, b int) bool {
		ta, tb := tokensIn(s.usage.Last30d, out[a]), tokensIn(s.usage.Last30d, out[b])
		if ta != tb {
			return ta > tb
		}
		return out[a] < out[b]
	})
	return out
}

// sortedModels is Models' list: thirty days of usage first, then by name.
func (s snapshot) sortedModels() []modelInfo {
	out := append([]modelInfo(nil), s.models...)
	var last30 api.Period
	if s.usage != nil {
		last30 = s.usage.Last30d
	}
	sort.SliceStable(out, func(a, b int) bool {
		ta, tb := tokensIn(last30, out[a].id), tokensIn(last30, out[b].id)
		if ta != tb {
			return ta > tb
		}
		return out[a].id < out[b].id
	})
	return out
}

// modelIDs is every model the key can call, for spotting new ones.
func (s snapshot) modelIDs() []string {
	out := make([]string, 0, len(s.models))
	for _, m := range s.models {
		out = append(out, m.id)
	}
	return out
}

// installedTools is the supported tools found on this machine.
func (s snapshot) installedTools() []tools.Status { return tools.Installed(s.tools) }
