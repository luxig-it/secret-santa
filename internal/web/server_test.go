package web

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/luxig-it/secret-santa/internal/config"
	"github.com/luxig-it/secret-santa/internal/store"
)

const orgSecret = "test-organizer-secret"

type harness struct {
	t   *testing.T
	srv *httptest.Server
	st  *store.Store
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "web.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := config.Config{
		BaseURL:    "http://example.test",
		Organizers: []config.Organizer{{Label: "carsten", Token: orgSecret}},
	}
	srv := httptest.NewServer(New(cfg, st, slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(srv.Close)
	return &harness{t: t, srv: srv, st: st}
}

// client returns a cookie-aware client that does not follow redirects, so
// tests can look at where a form post sends them.
func (h *harness) client() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
}

func (h *harness) get(c *http.Client, path string) (int, string) {
	h.t.Helper()
	res, err := c.Get(h.srv.URL + path)
	if err != nil {
		h.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

// post submits a form and returns the status and Location header.
func (h *harness) post(c *http.Client, path string, form url.Values) (int, string) {
	h.t.Helper()
	res, err := c.PostForm(h.srv.URL+path, form)
	if err != nil {
		h.t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode, res.Header.Get("Location")
}

var tokenIn = regexp.MustCompile(`^/(?:o|p)/([A-Za-z0-9_-]+)`)

func tokenOf(t *testing.T, location string) string {
	t.Helper()
	m := tokenIn.FindStringSubmatch(location)
	if m == nil {
		t.Fatalf("no token in %q", location)
	}
	return m[1]
}

// setup creates a campaign as the organizer and returns its organizer and
// join tokens.
func (h *harness) setup() (org string, join string) {
	h.t.Helper()
	c := h.client()
	if code, loc := h.post(c, "/login", url.Values{"token": {orgSecret}}); code != 303 || loc != "/organizer" {
		h.t.Fatalf("login: %d %q", code, loc)
	}
	_, loc := h.post(c, "/organizer/campaigns", url.Values{"title": {"Büro"}, "budget": {"20 €"}, "draw_date": {"2026-12-01"}})
	org = tokenOf(h.t, loc)
	camp, err := h.st.CampaignByOrganizerToken(h.t.Context(), org)
	if err != nil {
		h.t.Fatal(err)
	}
	return org, camp.JoinToken
}

func (h *harness) join(joinToken, name string) string {
	h.t.Helper()
	code, loc := h.post(h.client(), "/j/"+joinToken, url.Values{"name": {name}})
	if code != 303 {
		h.t.Fatalf("join %s: %d", name, code)
	}
	return tokenOf(h.t, loc)
}

func TestLoginRequired(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	if code, loc := h.post(c, "/organizer/campaigns", url.Values{"title": {"x"}}); code != 303 || loc != "/" {
		t.Fatalf("create without login: %d %q", code, loc)
	}
	if code, _ := h.post(c, "/login", url.Values{"token": {"wrong-secret"}}); code != 401 {
		t.Fatalf("bad login: %d", code)
	}
}

func TestUnknownTokensAre404(t *testing.T) {
	h := newHarness(t)
	for _, p := range []string{"/o/nope", "/j/nope", "/p/nope", "/p/nope/s/1", "/nothing"} {
		if code, _ := h.get(h.client(), p); code != 404 {
			t.Errorf("%s: %d", p, code)
		}
	}
}

func TestSecretsStaySecret(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	org, join := h.setup()
	anna := h.join(join, "Anna")
	ben := h.join(join, "Ben")
	carl := h.join(join, "Carl")

	pBen, _ := h.st.ParticipantByToken(t.Context(), ben)

	h.post(c, "/p/"+anna+"/wish", url.Values{"wishlist": {"ANNA-WISH"}})
	h.post(c, "/p/"+ben+"/wish", url.Values{"wishlist": {"BEN-WISH"}})
	h.post(c, "/p/"+carl+"/wish", url.Values{"wishlist": {"CARL-WISH"}})

	// Anna and Carl each leave an idea for Ben.
	if code, loc := h.post(c, "/p/"+anna+"/s/"+itoa(pBen.ID), url.Values{"body": {"ANNA-FOR-BEN"}}); code != 303 || !strings.Contains(loc, "n=surprise") {
		t.Fatalf("save surprise: %d %q", code, loc)
	}
	h.post(c, "/p/"+carl+"/s/"+itoa(pBen.ID), url.Values{"body": {"CARL-FOR-BEN"}})

	secrets := []string{"ANNA-WISH", "BEN-WISH", "CARL-WISH", "ANNA-FOR-BEN", "CARL-FOR-BEN"}
	mustLack := func(page, body string, words ...string) {
		t.Helper()
		for _, w := range words {
			if strings.Contains(body, w) {
				t.Errorf("%s shows %s", page, w)
			}
		}
	}

	// Before the draw: everybody sees Ben has two surprises, nobody but the
	// author sees their content, and nobody sees other people's wishes.
	_, page := h.get(c, "/p/"+ben)
	if !strings.Contains(page, "2 Überraschungen") {
		t.Error("Ben's page does not mention his two surprises")
	}
	mustLack("Ben's page", page, "ANNA-WISH", "CARL-WISH", "ANNA-FOR-BEN", "CARL-FOR-BEN")

	_, page = h.get(c, "/p/"+anna)
	mustLack("Anna's page", page, "BEN-WISH", "CARL-WISH", "ANNA-FOR-BEN", "CARL-FOR-BEN")
	_, page = h.get(c, "/p/"+anna+"/s/"+itoa(pBen.ID))
	if !strings.Contains(page, "ANNA-FOR-BEN") {
		t.Error("Anna cannot read her own idea")
	}
	mustLack("Anna's editor", page, "CARL-FOR-BEN")

	_, page = h.get(c, "/j/"+join)
	mustLack("join page", page, secrets...)
	_, page = h.get(c, "/o/"+org)
	mustLack("organizer page", page, secrets...)
	if !strings.Contains(page, "3 von 3 bereit") {
		t.Error("organizer page does not report everyone ready")
	}

	// Ben may not open an editor for himself.
	if code, _ := h.get(c, "/p/"+ben+"/s/"+itoa(pBen.ID)); code != 404 {
		t.Errorf("self surprise page: %d", code)
	}

	if code, loc := h.post(c, "/o/"+org+"/draw", nil); code != 303 || !strings.Contains(loc, "n=drawn") {
		t.Fatalf("draw: %d %q", code, loc)
	}

	// After the draw: Ben's giver sees Ben's wish and both ideas; Ben sees
	// neither idea; the organizer still sees nothing.
	camp, _ := h.st.CampaignByOrganizerToken(t.Context(), org)
	for _, tok := range []string{anna, ben, carl} {
		p, _ := h.st.ParticipantByToken(t.Context(), tok)
		r, _ := h.st.Receiver(t.Context(), camp.ID, p.ID)
		_, page := h.get(c, "/p/"+tok)
		if !strings.Contains(page, "Du beschenkst") || !strings.Contains(page, strings.ToUpper(r.Name)+"-WISH") {
			t.Errorf("%s does not see %s's wish", p.Name, r.Name)
		}
		if r.ID == pBen.ID {
			if !strings.Contains(page, "ANNA-FOR-BEN") || !strings.Contains(page, "CARL-FOR-BEN") {
				t.Errorf("Ben's giver %s misses ideas", p.Name)
			}
		} else {
			mustLack(p.Name+"'s result", page, "ANNA-FOR-BEN", "CARL-FOR-BEN")
		}
	}
	_, page = h.get(c, "/o/"+org)
	mustLack("organizer page after draw", page, secrets...)

	// Changes after the draw are refused.
	if _, loc := h.post(c, "/p/"+anna+"/wish", url.Values{"wishlist": {"changed"}}); !strings.Contains(loc, "drawn-already") {
		t.Errorf("wish after draw: %q", loc)
	}
	if code, _ := h.post(h.client(), "/j/"+join, url.Values{"name": {"Dora"}}); code != 409 {
		t.Errorf("late join: %d", code)
	}
}

func TestDrawWaitsForWishes(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	org, join := h.setup()
	anna := h.join(join, "Anna")
	h.join(join, "Ben")
	h.post(c, "/p/"+anna+"/wish", url.Values{"wishlist": {"x"}})
	if _, loc := h.post(c, "/o/"+org+"/draw", nil); !strings.Contains(loc, "not-ready") {
		t.Fatalf("draw with a missing wish: %q", loc)
	}
	_, page := h.get(c, "/o/"+org)
	if !strings.Contains(page, "1 von 2 bereit") || !strings.Contains(page, "disabled") {
		t.Error("organizer page does not show the draw as blocked")
	}
}

func TestSurpriseSwitch(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	_, join := h.setup()
	anna := h.join(join, "Anna")
	ben := h.join(join, "Ben")
	pBen, _ := h.st.ParticipantByToken(t.Context(), ben)

	// Hidden "0" plus unticked checkbox: closed.
	h.post(c, "/p/"+ben+"/surprises", url.Values{"open": {"0"}})
	if _, loc := h.post(c, "/p/"+anna+"/s/"+itoa(pBen.ID), url.Values{"body": {"x"}}); !strings.Contains(loc, "closed-already") {
		t.Fatalf("surprise for a closed recipient: %q", loc)
	}
	_, page := h.get(c, "/p/"+anna)
	if strings.Contains(page, "Überraschung hinterlegen") {
		t.Error("Anna is offered to leave a surprise for closed Ben")
	}

	// Hidden "0" plus ticked "1": open again.
	h.post(c, "/p/"+ben+"/surprises", url.Values{"open": {"0", "1"}})
	if _, loc := h.post(c, "/p/"+anna+"/s/"+itoa(pBen.ID), url.Values{"body": {"x"}}); !strings.Contains(loc, "n=surprise") {
		t.Fatalf("surprise for an open recipient: %q", loc)
	}
}

func TestDuplicateNameIsExplained(t *testing.T) {
	h := newHarness(t)
	_, join := h.setup()
	h.join(join, "Anna")
	if code, _ := h.post(h.client(), "/j/"+join, url.Values{"name": {"  anna "}}); code != 409 {
		t.Fatalf("duplicate join: %d", code)
	}
}

func TestPreviewSanitises(t *testing.T) {
	h := newHarness(t)
	_, join := h.setup()
	anna := h.join(join, "Anna")
	res, err := http.PostForm(h.srv.URL+"/p/"+anna+"/preview", url.Values{"text": {"**hi** <script>x</script>"}})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(b), "<strong>hi</strong>") || strings.Contains(string(b), "<script>") {
		t.Fatalf("preview = %q", b)
	}
	if res, _ := http.PostForm(h.srv.URL+"/p/nope/preview", url.Values{"text": {"x"}}); res.StatusCode != 404 {
		t.Fatalf("preview without a valid link: %d", res.StatusCode)
	}
}

func TestSecurityHeaders(t *testing.T) {
	h := newHarness(t)
	res, err := http.Get(h.srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	for k, want := range map[string]string{
		"Referrer-Policy": "no-referrer",
		"Cache-Control":   "no-store",
	} {
		if got := res.Header.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if !strings.Contains(res.Header.Get("Content-Security-Policy"), "default-src 'self'") {
		t.Error("CSP missing")
	}
}

func itoa(id int64) string { return strconv.FormatInt(id, 10) }

func TestOversizedFormDoesNotDelete(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	_, join := h.setup()
	anna := h.join(join, "Anna")
	h.post(c, "/p/"+anna+"/wish", url.Values{"wishlist": {"KEEP-ME"}})

	huge := url.Values{"wishlist": {"x"}, "padding": {strings.Repeat("a", maxForm)}}
	if code, _ := h.post(c, "/p/"+anna+"/wish", huge); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized post: %d", code)
	}
	p, _ := h.st.ParticipantByToken(t.Context(), anna)
	if p.Wishlist != "KEEP-ME" {
		t.Fatalf("wish after oversized post = %q", p.Wishlist)
	}
}

func TestLoginThrottle(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	for i := range loginFailures {
		if code, _ := h.post(c, "/login", url.Values{"token": {"wrong-secret"}}); code != 401 {
			t.Fatalf("attempt %d: %d", i, code)
		}
	}
	if code, _ := h.post(c, "/login", url.Values{"token": {orgSecret}}); code != http.StatusTooManyRequests {
		t.Fatalf("after %d failures: %d", loginFailures, code)
	}
}

func TestStaticFiles(t *testing.T) {
	h := newHarness(t)
	for path, want := range map[string]int{
		"/static/app.css":    200,
		"/static/":           404,
		"/static/missing.js": 404,
		"/static/../go.mod":  404,
	} {
		res, err := http.Get(h.srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != want {
			t.Errorf("%s: %d, want %d", path, res.StatusCode, want)
		}
		if res.StatusCode == 404 && strings.Contains(res.Header.Get("Cache-Control"), "immutable") {
			t.Errorf("%s: 404 cached as immutable", path)
		}
	}
}

func TestSurpriseAcrossCampaigns(t *testing.T) {
	h := newHarness(t)
	_, join1 := h.setup()
	_, join2 := h.setup()
	anna := h.join(join1, "Anna")
	stranger := h.join(join2, "Stranger")
	ps, _ := h.st.ParticipantByToken(t.Context(), stranger)
	if code, _ := h.get(h.client(), "/p/"+anna+"/s/"+itoa(ps.ID)); code != 404 {
		t.Fatalf("editor for someone in another campaign: %d", code)
	}
}

func TestAdminLinkShown(t *testing.T) {
	h := newHarness(t)
	org, _ := h.setup()
	c := h.client()
	h.post(c, "/login", url.Values{"token": {orgSecret}})
	adminURL := "http://example.test/o/" + org
	if _, page := h.get(c, "/o/"+org); !strings.Contains(page, `value="`+adminURL+`"`) {
		t.Error("manage page lacks the admin link field")
	}
	if _, page := h.get(c, "/organizer"); !strings.Contains(page, `data-copy-value="`+adminURL+`"`) {
		t.Error("campaign list lacks the copy button for the admin link")
	}
}

func TestNeedsTimeSwitch(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	org, join := h.setup()
	anna := h.join(join, "Anna")
	ben := h.join(join, "Ben")
	h.post(c, "/p/"+anna+"/wish", url.Values{"wishlist": {"x"}})

	// Hidden "0" plus ticked "1": Ben wants more time.
	if _, loc := h.post(c, "/p/"+ben+"/wish", url.Values{"wishlist": {"y"}, "needs_time": {"0", "1"}}); !strings.Contains(loc, "wish-thinking") {
		t.Fatalf("save with time to think: %q", loc)
	}
	if _, page := h.get(c, "/o/"+org); !strings.Contains(page, "1 von 2 bereit") || !strings.Contains(page, "Braucht noch Bedenkzeit") {
		t.Error("organizer page does not show Ben as thinking")
	}
	if _, loc := h.post(c, "/o/"+org+"/draw", nil); !strings.Contains(loc, "not-ready") {
		t.Fatalf("draw while Ben thinks: %q", loc)
	}

	// Clearing the wish drops the time to think with it.
	h.post(c, "/p/"+ben+"/wish", url.Values{"wishlist": {""}, "needs_time": {"0", "1"}})
	if p, _ := h.st.ParticipantByToken(t.Context(), ben); p.NeedsTime {
		t.Error("an empty wish kept needs_time")
	}

	// Only the hidden "0": the switch is off again.
	h.post(c, "/p/"+ben+"/wish", url.Values{"wishlist": {"y"}, "needs_time": {"0"}})
	if _, loc := h.post(c, "/o/"+org+"/draw", nil); !strings.Contains(loc, "n=drawn") {
		t.Fatalf("draw after Ben decided: %q", loc)
	}
}

func TestSurpriseSuffices(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	org, join := h.setup()
	anna := h.join(join, "Anna")
	ben := h.join(join, "Ben")
	pBen, _ := h.st.ParticipantByToken(t.Context(), ben)
	h.post(c, "/p/"+anna+"/wish", url.Values{"wishlist": {"x"}})

	if _, loc := h.post(c, "/p/"+ben+"/surprises", url.Values{"open": {"0", "1"}, "suffices": {"0", "1"}}); !strings.Contains(loc, "n=suffices") {
		t.Fatalf("switch on: %q", loc)
	}
	if _, page := h.get(c, "/o/"+org); !strings.Contains(page, "Wartet auf eine Überraschung") || !strings.Contains(page, "1 von 2 bereit") {
		t.Error("organizer page does not show Ben waiting for a surprise")
	}

	h.post(c, "/p/"+anna+"/s/"+itoa(pBen.ID), url.Values{"body": {"IDEA-FOR-BEN"}})
	if _, page := h.get(c, "/p/"+ben); !strings.Contains(page, "auch ohne eigenen Wunsch bereit") || strings.Contains(page, "IDEA-FOR-BEN") {
		t.Errorf("Ben's page does not confirm readiness, or leaks the idea: %v %v", strings.Contains(page, "auch ohne eigenen Wunsch bereit"), strings.Contains(page, "IDEA-FOR-BEN"))
	}
	if _, loc := h.post(c, "/o/"+org+"/draw", nil); !strings.Contains(loc, "n=drawn") {
		t.Fatalf("draw: %q", loc)
	}
	// Anna drew Ben: she learns he has no wish and sees the idea.
	if _, page := h.get(c, "/p/"+anna); !strings.Contains(page, "keinen eigenen Wunsch") || !strings.Contains(page, "IDEA-FOR-BEN") {
		t.Error("Anna's result does not explain the missing wish or lacks the idea")
	}
}

func TestClosingSurprisesDropsSuffices(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	_, join := h.setup()
	ben := h.join(join, "Ben")
	h.post(c, "/p/"+ben+"/surprises", url.Values{"open": {"0", "1"}, "suffices": {"0", "1"}})
	// Closing: the second switch is not rendered then, so it is not sent.
	if _, loc := h.post(c, "/p/"+ben+"/surprises", url.Values{"open": {"0"}}); !strings.Contains(loc, "n=closed-suffices") {
		t.Fatalf("close: %q", loc)
	}
	if p, _ := h.st.ParticipantByToken(t.Context(), ben); p.SurpriseSuffices {
		t.Error("surprise still suffices after closing surprises")
	}
}

func TestClosedSurprisesHiddenAfterDraw(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	org, join := h.setup()
	anna := h.join(join, "Anna")
	ben := h.join(join, "Ben")
	pBen, _ := h.st.ParticipantByToken(t.Context(), ben)
	h.post(c, "/p/"+anna+"/wish", url.Values{"wishlist": {"x"}})
	h.post(c, "/p/"+ben+"/wish", url.Values{"wishlist": {"BEN-REAL-WISH"}})
	h.post(c, "/p/"+anna+"/s/"+itoa(pBen.ID), url.Values{"body": {"HIDDEN-IDEA"}})
	h.post(c, "/p/"+ben+"/surprises", url.Values{"open": {"0"}})

	if _, page := h.get(c, "/p/"+anna); strings.Contains(page, "1 Überraschung hinterlegt") || !strings.Contains(page, "Keine Überraschungen gewünscht") {
		t.Error("group still counts ideas for Ben after he closed surprises")
	}
	if _, page := h.get(c, "/p/"+anna+"/s/"+itoa(pBen.ID)); !strings.Contains(page, "möchte gerade keine Überraschungen") {
		t.Error("Anna's editor does not tell her that her idea will not be shown")
	}
	if _, page := h.get(c, "/p/"+ben); !strings.Contains(page, "Solange du Überraschungen ausgeschaltet hast") {
		t.Error("Ben is not told his stored idea is hidden")
	}
	h.post(c, "/o/"+org+"/draw", nil)
	if _, page := h.get(c, "/p/"+ben); !strings.Contains(page, "hat dein Wichtel nur deinen Wunsch gesehen") {
		t.Error("Ben's page after the draw does not use the final wording")
	}
	_, page := h.get(c, "/p/"+anna)
	if !strings.Contains(page, "BEN-REAL-WISH") || strings.Contains(page, "HIDDEN-IDEA") || !strings.Contains(page, "wollte keine Überraschungen") {
		t.Error("Anna's result must show Ben's wish and not the idea he closed")
	}
}

func TestWishlessOpensSurprisesAndAsksForHelp(t *testing.T) {
	h := newHarness(t)
	c := h.client()
	org, join := h.setup()
	anna := h.join(join, "Anna")
	ben := h.join(join, "Ben")
	carla := h.join(join, "Carla")
	pBen, _ := h.st.ParticipantByToken(t.Context(), ben)

	// Ben closes surprises, then flips "nothing comes to mind": that wins
	// and opens surprises again.
	h.post(c, "/p/"+ben+"/surprises", url.Values{"open": {"0"}})
	if _, loc := h.post(c, "/p/"+ben+"/surprises", url.Values{"open": {"0"}, "suffices": {"0", "1"}}); !strings.Contains(loc, "n=suffices-opened") {
		t.Fatalf("switch on while closed: %q", loc)
	}
	if p, _ := h.st.ParticipantByToken(t.Context(), ben); !p.SurprisesOpen || !p.SurpriseSuffices {
		t.Fatalf("open=%v suffices=%v, want both on", p.SurprisesOpen, p.SurpriseSuffices)
	}

	banner := "Ben selbst ist wunschlos glücklich"
	if _, page := h.get(c, "/p/"+anna); !strings.Contains(page, banner) || !strings.Contains(page, "member-help") {
		t.Error("Anna sees no banner or highlight for Ben")
	}
	if _, page := h.get(c, "/p/"+ben); strings.Contains(page, banner) {
		t.Error("Ben is asked to help himself")
	}

	// Once Anna helped, her banner goes; Carla still sees it.
	h.post(c, "/p/"+anna+"/s/"+itoa(pBen.ID), url.Values{"body": {"idea"}})
	if _, page := h.get(c, "/p/"+anna); strings.Contains(page, banner) {
		t.Error("Anna still asked after leaving an idea")
	}
	if _, page := h.get(c, "/p/"+carla); !strings.Contains(page, banner) {
		t.Error("Carla is no longer asked although she has not helped")
	}

	// After the draw nobody is asked any more.
	h.post(c, "/p/"+anna+"/wish", url.Values{"wishlist": {"x"}})
	h.post(c, "/p/"+carla+"/wish", url.Values{"wishlist": {"y"}})
	if _, loc := h.post(c, "/o/"+org+"/draw", nil); !strings.Contains(loc, "n=drawn") {
		t.Fatalf("draw: %q", loc)
	}
	if _, page := h.get(c, "/p/"+carla); strings.Contains(page, banner) || strings.Contains(page, "member-help") {
		t.Error("banner or highlight shown after the draw")
	}
}

func TestResolveSurpriseSwitches(t *testing.T) {
	off := store.Participant{}
	both := store.Participant{SurprisesOpen: true, SurpriseSuffices: true}
	cases := []struct {
		name           string
		changed        string
		open, suffices bool
		before         store.Participant
		wantOpen, want bool
	}{
		{"suffices on opens", "suffices", false, true, off, true, true},
		{"closing ends suffices", "open", false, true, both, false, false},
		// A stale tab still showing both off, where the user turns on
		// suffices although the stored row already has it.
		{"stale tab, suffices flipped", "suffices", false, true, both, true, true},
		{"no JS: suffices newly on", "", false, true, off, true, true},
		{"no JS: open flipped off", "", false, true, both, false, false},
	}
	for _, c := range cases {
		open, suffices := resolveSurpriseSwitches(c.changed, c.open, c.suffices, c.before)
		if open != c.wantOpen || suffices != c.want {
			t.Errorf("%s: got open=%v suffices=%v, want %v %v", c.name, open, suffices, c.wantOpen, c.want)
		}
	}
}
