package pulse

import (
	"strings"
	"time"

	"github.com/helmcode/nan-cli/internal/api"
)

// shorten keeps the first six characters of an identifier: enough to
// recognise it, not enough to use it. A screen is shared, recorded and
// screenshotted more often than not.
func shorten(value string) string {
	const keep = 6
	runes := []rune(value)
	if len(runes) <= keep {
		return "..."
	}
	return string(runes[:keep]) + "..."
}

// day is an RFC 3339 time as a date, or the text as it came if it is not one.
func day(value string) string {
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t.Local().Format("2 Jan 2006")
	}
	return value
}

// shortenEmail shortens the part before the @ and keeps the domain: the
// domain is what tells a work account from a personal one, which is the
// question a member asks of this line, and it identifies nobody on its own.
func shortenEmail(email string) string {
	local, domain, ok := strings.Cut(email, "@")
	if !ok || domain == "" {
		return shorten(email)
	}
	return shorten(local) + "@" + domain
}

// profileRows is the account as the member would say it. api.Me decodes only
// what is listed, so a key the platform adds stays off the screen until
// somebody chooses it, and a field that is empty leaves no row. An ended
// membership has no plan to show: the tier persists through grace, and
// "Inference" beside "Ended" would read as access.
func profileRows(me *api.Me) []row {
	ended := me.State == "grace" || me.State == "churned" || me.State == "expired"
	var name, role string
	if p := me.Profile; p != nil {
		name, role = strings.TrimSpace(p.FullName), p.JobRole
		if role == "other" {
			role = p.JobRoleOther
		}
	}
	plan := ""
	if !ended {
		plan = planName(me.Tier)
	}
	var out []row
	for _, f := range [][2]string{
		{"Name", name},
		{"Email", shortenEmail(me.Email)},
		{"Plan", plan},
		{"Membership", membershipState(me.State)},
		// graceUntil is when the apps and data go, not when access ends:
		// inference stopped with the membership.
		{"Data kept until", day(me.GraceUntil)},
		{"Job role", strings.TrimSpace(role)},
	} {
		if f[1] == "" || (f[0] == "Email" && me.Email == "") {
			continue
		}
		out = append(out, row{id: "field:" + strings.ToLower(strings.ReplaceAll(f[0], " ", "-")), cells: []string{f[0], f[1]}})
	}
	return out
}

func planName(tier string) string {
	if tier == "" || tier == "none" {
		return ""
	}
	return strings.ToUpper(tier[:1]) + tier[1:]
}

// membershipState words the platform's lifecycle state as the web does:
// grace is the only state cloud-ui calls an ended membership, churned and
// expired are the same thing further on.
func membershipState(state string) string {
	switch state {
	case "":
		return ""
	case "subscribed":
		return "Active"
	case "free":
		return "Free"
	case "grace", "churned", "expired":
		return "Ended"
	case "registered", "invited":
		return "Not subscribed"
	}
	r := []rune(strings.ReplaceAll(state, "_", " "))
	return strings.ToUpper(string(r[:1])) + string(r[1:])
}
