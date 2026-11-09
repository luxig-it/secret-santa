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
