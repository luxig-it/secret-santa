// Package views holds the HTML templates. Everything a page shows is passed
// in explicitly; the templates never reach into the store themselves, which
// keeps the decision about who may see what in the handlers.
package views

import (
	"fmt"
	"time"

	"github.com/luxig-it/secret-santa/internal/store"
)

// AssetVersion is appended to static URLs so browsers fetch new files after
// an update instead of serving the cached ones. The web package sets it from
// a hash of the embedded files.
var AssetVersion string

// Asset returns the URL of a static file.
func Asset(name string) string { return "/static/" + name + "?v=" + AssetVersion }

// Flash is a one-line notice shown at the top of a page.
type Flash struct {
	Kind string // "ok" or "error"
	Text string
}

// Member is one row of the group overview. The counts are visible to
// everybody, the contents behind them are not.
type Member struct {
	store.Participant
	Surprises int
	// IsMe marks the viewer's own row on a participant page.
	IsMe bool
	// ByMe reports that the viewer left an idea for this person.
	ByMe bool
}

// Ready mirrors the draw's precondition.
func (m Member) Ready() bool { return m.ReadyWith(m.Surprises) }

// AwaitsSurprise marks somebody who will be ready as soon as anybody leaves
// a surprise idea for them – worth pointing out to the rest of the group.
func (m Member) AwaitsSurprise() bool {
	return !m.Ready() && m.Participant.ReadyWith(m.Surprises+1)
}

// SurpriseInsteadOfWish marks somebody who is ready only thanks to the
// surprise ideas left for them.
func (m Member) SurpriseInsteadOfWish() bool { return m.Ready() && !m.HasWish() }

// CampaignPage is what every page about one campaign needs.
type CampaignPage struct {
	Campaign store.Campaign
	Members  []Member
	Flash    Flash
}

// ReadyCount reports how many members are ready.
func (p CampaignPage) ReadyCount() int {
	n := 0
	for _, m := range p.Members {
		if m.Ready() {
			n++
		}
	}
	return n
}

// AllReady reports whether the draw may happen.
func (p CampaignPage) AllReady() bool {
	return len(p.Members) >= 2 && p.ReadyCount() == len(p.Members)
}

// ManagePage is the organizer's view of a campaign.
type ManagePage struct {
	CampaignPage
	JoinURL    string
	AdminURL   string
	Exclusions []store.Exclusion
}

// JoinPage invites people into a campaign.
type JoinPage struct {
	CampaignPage
	Name string
}

// ParticipantPage is somebody's private page.
type ParticipantPage struct {
	CampaignPage
	Me    store.Participant
	MyURL string
	// Set after the draw only.
	Receiver  *store.Participant
	Surprises []store.Surprise
}

// MeMember is the viewer's own row, with the counts the readiness rules need.
func (p ParticipantPage) MeMember() Member {
	for _, m := range p.Members {
		if m.IsMe {
			return m
		}
	}
	return Member{Participant: p.Me}
}

// MySurpriseCount is how many ideas others left for the viewer.
func (p ParticipantPage) MySurpriseCount() int { return p.MeMember().Surprises }

// SurprisePage edits the idea the viewer leaves for somebody.
type SurprisePage struct {
	Campaign  store.Campaign
	Me        store.Participant
	Recipient store.Participant
	Body      string
	Exists    bool
	Flash     Flash
}

// OrganizerPage lists an organizer's campaigns.
type OrganizerPage struct {
	BaseURL   string
	Label     string
	Campaigns []store.Campaign
	Flash     Flash
	Form      CampaignForm
}

// CampaignForm keeps the input of a rejected create request.
type CampaignForm struct {
	Title, Budget, DrawDate string
}

var months = [...]string{"Januar", "Februar", "März", "April", "Mai", "Juni",
	"Juli", "August", "September", "Oktober", "November", "Dezember"}

// GermanDate turns 2026-12-01 into "1. Dezember 2026" and leaves anything it
// does not understand alone.
func GermanDate(iso string) string {
	t, err := time.Parse(time.DateOnly, iso)
	if err != nil {
		return iso
	}
	return fmt.Sprintf("%d. %s %d", t.Day(), months[t.Month()-1], t.Year())
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func participantPath(token string, rest string) string { return "/p/" + token + rest }
func organizerPath(token string, rest string) string   { return "/o/" + token + rest }

// OrganizerURL is the absolute management link of a campaign.
func OrganizerURL(base, token string) string { return base + organizerPath(token, "") }
func joinPath(token string) string           { return "/j/" + token }
func idPath(prefix string, id int64, rest string) string {
	return fmt.Sprintf("%s/%d%s", prefix, id, rest)
}
