package tui

import (
	"regexp"
	"strings"
	"testing"
)

var profileANSI = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]`)

// profileRows renders the Profile tab and returns its "Label: value" rows,
// keyed by label, with colours and padding taken out.
func profileRows(t *testing.T, me map[string]any) (map[string]string, string) {
	t.Helper()
	out := profileANSI.ReplaceAllString(renderProfile(me, newLayout(90, 30)), "")
	rows := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		label, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok || strings.Contains(label, " sign out") {
			continue
		}
		rows[label] = strings.TrimSpace(value)
	}
	return rows, out
}

// A full /api/auth/me body as the platform sends it today, internal fields
// and all.
func fullMe() map[string]any {
	return map[string]any{
		"discordId":  "123456789012345678",
		"userUUID":   "6f1c2a7e-0000-4000-8000-000000000001",
		"email":      "bperez@example.com",
		"handle":     "bperez",
		"username":   "bperez",
		"namespace":  "member-bperez",
		"roles":      []any{"member"},
		"isAdmin":    false,
		"role":       "member",
		"tier":       "inference",
		"state":      "grace",
		"graceUntil": "2026-12-01T00:00:00Z",
		"profile": map[string]any{
			"fullName":     "Bea Pérez",
			"jobRole":      "engineer",
			"jobRoleOther": "",
			"xUrl":         "https://x.com/bperez",
			"linkedinUrl":  "",
		},
		"entitlements":      map[string]any{"inference": true},
		"entitlementsError": false,
		"region":            "eu",
		"expiresAt":         "2026-11-24T10:00:00Z",
		"features":          map[string]any{"ephemeral": true},
		"environment":       "production",
		"inferenceProfile":  "prod",
		"workspaces_beta":   false,
		"session":           map[string]any{"browser": false},
		"mfa":               map[string]any{"available": true},
		// A key the platform may add later: not shown until it is allowlisted.
		"someFutureField": "surprise",
	}
}

// The tab used to print every key of /api/auth/me with %v, so members saw
// `Grace Until: <nil>`, `Entitlements Error: false`, `Inference Profile: prod`.
// It now shows an allowlist of labelled, formatted fields and nothing else.
func TestProfileShowsOnlyAllowlistedFields(t *testing.T) {
	rows, out := profileRows(t, fullMe())

	want := map[string]string{
		"Name":         "Bea Pérez",
		"Handle":       "bperez",
		"Email":        "bperez...",
		"Plan":         "Inference",
		"Membership":   "Ended",
		"Access until": "1 Dec 2026",
		"Job role":     "engineer",
		"X":            "https://x.com/bperez",
	}
	for label, value := range want {
		if got, ok := rows[label]; !ok {
			t.Errorf("row %q is missing\n%s", label, out)
		} else if got != value {
			t.Errorf("row %q = %q, want %q", label, got, value)
		}
	}
	for label := range rows {
		if _, ok := want[label]; !ok {
			t.Errorf("row %q is not on the allowlist\n%s", label, out)
		}
	}
	for _, leak := range []string{
		"<nil>", "map[", "[member]",
		"Entitlements", "Inference Profile", "Environment", "production", "prod\n",
		"Discord", "UUID", "Namespace", "member-bperez", "Expires", "Features",
		"Region", "Workspaces", "Session", "Mfa", "Is Admin",
		"Some Future Field", "surprise",
		"6f1c2a7e", "123456789012345678",
	} {
		if strings.Contains(out, leak) {
			t.Errorf("the Profile tab shows %q\n%s", leak, out)
		}
	}
}

// Missing, null and empty values leave no row behind: no `Label:` with
// nothing after it, no "<nil>".
func TestProfileSkipsMissingFields(t *testing.T) {
	rows, out := profileRows(t, map[string]any{
		"handle":     "bperez",
		"email":      "",
		"tier":       nil,
		"state":      "subscribed",
		"graceUntil": nil,
		"profile":    map[string]any{"fullName": "", "jobRole": nil},
	})
	want := map[string]string{"Handle": "bperez", "Membership": "Active"}
	if len(rows) != len(want) {
		t.Errorf("got rows %v, want exactly %v\n%s", rows, want, out)
	}
	for label, value := range want {
		if rows[label] != value {
			t.Errorf("row %q = %q, want %q", label, rows[label], value)
		}
	}
	if strings.Contains(out, "<nil>") {
		t.Errorf("the Profile tab shows <nil>\n%s", out)
	}
}

// No profile at all still renders, and still offers the sign-out.
func TestProfileWithEmptyBody(t *testing.T) {
	rows, out := profileRows(t, map[string]any{})
	if len(rows) != 0 {
		t.Errorf("an empty body rendered rows %v", rows)
	}
	if !strings.Contains(out, "sign out") {
		t.Error("the sign-out hint is gone")
	}
}

// "other" as a job role means the member typed their own; that is the one to
// show. A grace date that does not parse is not shown raw.
func TestProfileFieldFormatting(t *testing.T) {
	rows, _ := profileRows(t, map[string]any{
		"tier":       "community",
		"state":      "subscribed",
		"graceUntil": "not-a-date",
		"profile": map[string]any{
			"jobRole":      "other",
			"jobRoleOther": "Founder",
			"linkedinUrl":  "https://linkedin.com/in/bperez",
		},
	})
	for label, value := range map[string]string{
		"Plan":       "Community",
		"Membership": "Active",
		"Job role":   "Founder",
		"LinkedIn":   "https://linkedin.com/in/bperez",
	} {
		if rows[label] != value {
			t.Errorf("row %q = %q, want %q", label, rows[label], value)
		}
	}
	if _, ok := rows["Access until"]; ok {
		t.Errorf("an unparsable grace date was shown: %q", rows["Access until"])
	}
}

// keyW is 12 at 40 columns and "Access until:" is 13; lipgloss wraps a label
// that does not fit, which split the row over two lines.
func TestProfileLabelsDoNotWrapWhenNarrow(t *testing.T) {
	out := profileANSI.ReplaceAllString(renderProfile(fullMe(), newLayout(40, 30)), "")
	if !strings.Contains(out, "Access until:") {
		t.Errorf("the label wrapped at 40 columns\n%s", out)
	}
}

// The edges of the mapping: no plan is no row, an unmapped value reads as
// words, a non-string value in an allowlisted key is skipped, and "other"
// with nothing typed leaves no job role.
func TestProfileMappingEdges(t *testing.T) {
	rows, out := profileRows(t, map[string]any{
		"handle":  123,
		"tier":    "none",
		"state":   "host_unreachable",
		"profile": map[string]any{"jobRole": "other", "jobRoleOther": ""},
	})
	want := map[string]string{"Membership": "Host unreachable"}
	if len(rows) != len(want) || rows["Membership"] != want["Membership"] {
		t.Errorf("got rows %v, want exactly %v\n%s", rows, want, out)
	}
	if got := wordsOf(""); got != "" {
		t.Errorf("wordsOf(\"\") = %q, want empty", got)
	}
}
