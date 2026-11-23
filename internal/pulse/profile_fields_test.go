package pulse

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/helmcode/nan-cli/internal/api"
)

// asMap reads the rows as label -> value.
func asMap(rows []row) map[string]string {
	out := map[string]string{}
	for _, r := range rows {
		out[r.cells[0]] = r.cells[1]
	}
	return out
}

func TestProfileRowsByMembership(t *testing.T) {
	const grace = "2026-12-01T10:00:00Z"
	for _, tc := range []struct {
		name string
		me   api.Me
		want map[string]string
		not  []string
	}{
		{
			"active inference",
			api.Me{Email: "someone@example.com", Tier: "inference", State: "subscribed"},
			map[string]string{"Plan": "Inference", "Membership": "Active"},
			[]string{"Data kept until"},
		},
		{
			"gentle tier",
			api.Me{Email: "someone@example.com", Tier: "gentle", State: "subscribed"},
			map[string]string{"Plan": "Gentle", "Membership": "Active"},
			nil,
		},
		{
			"ended in grace",
			api.Me{Email: "someone@example.com", Tier: "inference", State: "grace", GraceUntil: grace},
			map[string]string{"Membership": "Ended"},
			[]string{"Plan", "Access until"},
		},
		{
			"grace without a date",
			api.Me{Email: "someone@example.com", Tier: "inference", State: "grace"},
			map[string]string{"Membership": "Ended"},
			[]string{"Plan", "Data kept until", "Access until"},
		},
		{
			"churned",
			api.Me{Email: "someone@example.com", Tier: "inference", State: "churned"},
			map[string]string{"Membership": "Ended"},
			[]string{"Plan", "Data kept until"},
		},
		{
			"expired",
			api.Me{Email: "someone@example.com", State: "expired"},
			map[string]string{"Membership": "Ended"},
			[]string{"Plan"},
		},
		{
			"free signup",
			api.Me{Email: "someone@example.com", Tier: "none", State: "free"},
			map[string]string{"Membership": "Free"},
			[]string{"Plan"},
		},
		{
			"registered",
			api.Me{Email: "someone@example.com", State: "registered"},
			map[string]string{"Membership": "Not subscribed"},
			nil,
		},
		{
			"invited",
			api.Me{Email: "someone@example.com", State: "invited"},
			map[string]string{"Membership": "Not subscribed"},
			nil,
		},
		{
			"unknown state reads as words",
			api.Me{Email: "someone@example.com", State: "host_unreachable"},
			map[string]string{"Membership": "Host unreachable"},
			nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := asMap(profileRows(&tc.me))
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("%s = %q, want %q (rows %v)", k, got[k], v, got)
				}
			}
			for _, k := range tc.not {
				if _, ok := got[k]; ok {
					t.Errorf("row %q is shown (rows %v)", k, got)
				}
			}
		})
	}
}

// graceUntil is when the apps and data go; inference is already gone, so it
// is never said to be when access ends.
func TestGraceUntilIsWhenDataIsKept(t *testing.T) {
	got := asMap(profileRows(&api.Me{Email: "a@example.com", State: "grace", GraceUntil: "2026-12-01T12:00:00Z"}))
	if got["Data kept until"] != "1 Dec 2026" {
		t.Errorf("rows %v", got)
	}
	for k := range got {
		if strings.Contains(strings.ToLower(k), "access") || strings.Contains(strings.ToLower(k), "ends") {
			t.Errorf("label %q promises access", k)
		}
	}
}

func TestProfileRowsShowNameAndRole(t *testing.T) {
	got := asMap(profileRows(&api.Me{Email: "a@example.com", Profile: &api.MeProfile{FullName: " Ada Lovelace ", JobRole: "other", JobRoleOther: "Mathematician"}}))
	if got["Name"] != "Ada Lovelace" || got["Job role"] != "Mathematician" {
		t.Errorf("rows %v", got)
	}
	got = asMap(profileRows(&api.Me{Email: "a@example.com", Profile: &api.MeProfile{JobRole: "engineer"}}))
	if got["Job role"] != "engineer" {
		t.Errorf("rows %v", got)
	}
	if _, ok := asMap(profileRows(&api.Me{Email: "a@example.com"}))["Name"]; ok {
		t.Error("an empty name leaves a row")
	}
}

// The allowlist: a body with everything the platform says decodes into only
// what is listed, so nothing else can reach the screen.
func TestProfileAllowlistKeepsUnknownKeysHidden(t *testing.T) {
	body := `{"email":"someone@example.com","tier":"inference","state":"subscribed","graceUntil":null,
		"userUUID":"u-123","discordId":"d-9","namespace":"ns-secret","environment":"production",
		"inferenceProfile":"prod","entitlementsError":false,"newThing":"surprise",
		"profile":{"fullName":"Ada","xUrl":"https://x.com/ada","internalNote":"nope"}}`
	var me api.Me
	if err := json.Unmarshal([]byte(body), &me); err != nil {
		t.Fatal(err)
	}
	rows := profileRows(&me)
	var all strings.Builder
	for _, r := range rows {
		all.WriteString(strings.Join(r.cells, " ") + "\n")
	}
	for _, leak := range []string{"u-123", "d-9", "ns-secret", "production", "prod", "surprise", "nope", "<nil>", "false", "x.com", "someone@example.com"} {
		if strings.Contains(all.String(), leak) {
			t.Errorf("%q is on Profile:\n%s", leak, all.String())
		}
	}
	if got := asMap(rows); len(got) != 4 { // Name, Email, Plan, Membership
		t.Errorf("rows %v", got)
	}
}

// The name and the role are typed by the member on the web, and they reach
// table cells, which keep escapes: they lose them on the way in, as every
// other name from the platform does.
func TestProfileTextLosesEscapes(t *testing.T) {
	me := cleanMe(&api.Me{Profile: &api.MeProfile{
		FullName: "Ada\x1b[2J", JobRole: "other", JobRoleOther: "dev\x1b]0;x\x07",
	}})
	for _, s := range []string{me.Profile.FullName, me.Profile.JobRole, me.Profile.JobRoleOther} {
		if strings.ContainsRune(s, 0x1b) {
			t.Errorf("%q kept an escape", s)
		}
	}
	if cleanMe(nil) != nil || cleanMe(&api.Me{}).Profile != nil {
		t.Error("cleanMe must pass nil and a missing profile through")
	}
}
