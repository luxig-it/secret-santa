// Package web is the HTTP layer: routing, input validation and the decision
// which page shows what to whom. Access is granted by the token in the URL
// alone, except for the organizer overview, which uses a cookie holding the
// organizer secret.
package web

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/a-h/templ"

	"github.com/luxig-it/secret-santa/internal/config"
	"github.com/luxig-it/secret-santa/internal/draw"
	"github.com/luxig-it/secret-santa/internal/markdown"
	"github.com/luxig-it/secret-santa/internal/store"
	"github.com/luxig-it/secret-santa/internal/web/views"
)

//go:embed static
var staticFiles embed.FS

var static = func() fs.FS {
	sub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		panic(err)
	}
	return sub
}()

func init() { views.AssetVersion = hashFiles(static) }

// Input limits. They keep the database small and the pages readable; the
// templates set the same maxlength so honest users never hit them.
const (
	maxTitle  = 100
	maxBudget = 50
	maxName   = 60
	maxText   = 5000
	maxForm   = 64 << 10
)

const organizerCookie = "organizer"

// Server holds what the handlers share.
type Server struct {
	cfg    config.Config
	store  *store.Store
	log    *slog.Logger
	secure bool
	logins *limiter
}

// New wires up every route.
func New(cfg config.Config, st *store.Store, log *slog.Logger) http.Handler {
	s := &Server{
		cfg:    cfg,
		store:  st,
		log:    log,
		secure: strings.HasPrefix(cfg.BaseURL, "https://"),
		logins: newLimiter(loginFailures, loginWindow),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /static/{file}", s.static)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok\n")) })

	mux.HandleFunc("GET /{$}", s.home)
	mux.HandleFunc("POST /login", s.login)
	mux.HandleFunc("POST /logout", s.logout)
	mux.HandleFunc("GET /organizer", s.organizer)
	mux.HandleFunc("POST /organizer/campaigns", s.createCampaign)

	mux.HandleFunc("GET /o/{token}", s.manage)
	mux.HandleFunc("POST /o/{token}/participants/{id}/delete", s.deleteParticipant)
	mux.HandleFunc("POST /o/{token}/exclusions", s.addExclusion)
	mux.HandleFunc("POST /o/{token}/exclusions/{id}/delete", s.deleteExclusion)
	mux.HandleFunc("POST /o/{token}/draw", s.draw)

	mux.HandleFunc("GET /j/{token}", s.joinPage)
	mux.HandleFunc("POST /j/{token}", s.join)

	mux.HandleFunc("GET /p/{token}", s.participant)
	mux.HandleFunc("POST /p/{token}/wish", s.saveWish)
	mux.HandleFunc("POST /p/{token}/surprises", s.setSurprisesOpen)
	mux.HandleFunc("GET /p/{token}/s/{id}", s.surprisePage)
	mux.HandleFunc("POST /p/{token}/s/{id}", s.saveSurprise)
	mux.HandleFunc("POST /p/{token}/preview", s.preview)

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { s.notFound(w, r) })

	return s.secureHeaders(s.limitBody(mux))
}

// secureHeaders applies to every response. Pages carry secrets in their URL,
// so they must never be sent along as a referrer, cached or framed.
func (s *Server) secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; "+
			"object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		if !strings.HasPrefix(r.URL.Path, "/static/") {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

// limitBody caps and parses every form up front. Handlers read fields with
// PostFormValue, which swallows parse errors and returns "", and an empty
// wish or idea means "delete" – so a broken or oversized body must be turned
// away here instead of reaching them.
func (s *Server) limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxForm)
		if r.Method == http.MethodPost {
			if err := r.ParseForm(); err != nil {
				status := http.StatusBadRequest
				var tooBig *http.MaxBytesError
				if errors.As(err, &tooBig) {
					status = http.StatusRequestEntityTooLarge
				}
				s.render(w, r, status, views.ErrorPage("Die Eingabe konnte nicht verarbeitet werden. Ist der Text zu lang?"))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// hashFiles fingerprints the embedded static files for cache busting.
func hashFiles(fsys fs.FS) string {
	h := sha256.New()
	_ = fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(fsys, path)
		if err != nil {
			return err
		}
		h.Write([]byte(path))
		h.Write(b)
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// static serves one embedded file. Only existing files are cached, and only
// for a long time when the URL carries the current content hash.
func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	info, err := fs.Stat(static, name)
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	if r.URL.Query().Get("v") == views.AssetVersion {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	http.ServeFileFS(w, r, static, name)
}

// --- rendering helpers -----------------------------------------------------

func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, c templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := c.Render(r.Context(), w); err != nil {
		s.log.Error("render", "path", r.URL.Path, "err", err)
	}
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusNotFound, views.NotFound())
}

// fail turns a store error into a response. Anything not anticipated is
// logged and shown as a generic error so internals do not leak.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	s.log.Error("request failed", "method", r.Method, "path", redactPath(r.URL.Path), "err", err)
	s.render(w, r, http.StatusInternalServerError, views.ErrorPage("Bitte versuche es gleich noch einmal."))
}

// redactPath keeps tokens out of the logs.
func redactPath(p string) string {
	parts := strings.Split(p, "/")
	if len(parts) > 2 && (parts[1] == "o" || parts[1] == "j" || parts[1] == "p") {
		parts[2] = "…"
	}
	return strings.Join(parts, "/")
}

// redirect answers a form post with 303 and a notice code the target page
// turns into a message, so a reload never resubmits the form.
func redirect(w http.ResponseWriter, r *http.Request, path, notice string) {
	if notice != "" {
		path += "?n=" + notice
	}
	http.Redirect(w, r, path, http.StatusSeeOther)
}

var notices = map[string]views.Flash{
	"created":               {Kind: "ok", Text: "Die Kampagne ist angelegt. Verschicke jetzt den Einladungslink."},
	"joined":                {Kind: "ok", Text: "Willkommen! Speichere jetzt deinen persönlichen Link und trag deinen Wunsch ein."},
	"wish":                  {Kind: "ok", Text: "Dein Wunsch ist gespeichert."},
	"wish-cleared":          {Kind: "ok", Text: "Dein Wunsch ist gelöscht. Ohne Wunsch bist du nicht bereit zur Auslosung."},
	"wish-cleared-surprise": {Kind: "ok", Text: "Dein Wunsch ist gelöscht. Dir genügt eine Überraschung – sobald eine für dich hinterlegt ist, bist du bereit."},
	"wish-thinking":         {Kind: "ok", Text: "Dein Wunsch ist gespeichert. Du hast noch Bedenkzeit und giltst erst als bereit, wenn du sie ausschaltest."},
	"open":                  {Kind: "ok", Text: "Andere können jetzt Überraschungen für dich hinterlegen."},
	"closed":                {Kind: "ok", Text: "Es können keine neuen Überraschungen mehr für dich hinterlegt werden."},
	"closed-suffices":       {Kind: "ok", Text: "Es können keine neuen Überraschungen mehr für dich hinterlegt werden. Damit genügt dir auch keine Überraschung mehr als Wunsch – trag einen Wunsch ein, um bereit zu sein."},
	"suffices":              {Kind: "ok", Text: "Alles klar: Sobald eine Überraschung für dich hinterlegt ist, bist du auch ohne eigenen Wunsch bereit."},
	"suffices-off":          {Kind: "ok", Text: "Du brauchst jetzt wieder einen eigenen Wunsch, um bereit zu sein."},
	"surprise":              {Kind: "ok", Text: "Deine Überraschung ist gespeichert."},
	"surprise-gone":         {Kind: "ok", Text: "Deine Überraschung ist gelöscht."},
	"removed":               {Kind: "ok", Text: "Die Person wurde entfernt."},
	"excluded":              {Kind: "ok", Text: "Der Ausschluss ist gespeichert."},
	"unexcluded":            {Kind: "ok", Text: "Der Ausschluss ist aufgehoben."},
	"drawn":                 {Kind: "ok", Text: "Ausgelost! Alle sehen jetzt über ihren Link, wen sie beschenken."},
	"drawn-already":         {Kind: "error", Text: "Die Kampagne ist bereits ausgelost, das ist nicht mehr möglich."},
	"not-ready":             {Kind: "error", Text: "Noch sind nicht alle bereit: Es fehlen Wünsche oder Überraschungen, oder jemand braucht noch Bedenkzeit."},
	"impossible":            {Kind: "error", Text: "Mit diesen Ausschlüssen ist keine Auslosung möglich. Hebe einen Ausschluss auf oder lade mehr Leute ein."},
	"too-few":               {Kind: "error", Text: "Für eine Auslosung braucht es mindestens zwei Personen."},
	"same-person":           {Kind: "error", Text: "Wähle zwei verschiedene Personen."},
	"too-long":              {Kind: "error", Text: "Der Text ist zu lang (höchstens 5000 Zeichen)."},
	"closed-already":        {Kind: "error", Text: "Diese Person möchte keine Überraschungen."},
}

func noticeFrom(r *http.Request) views.Flash { return notices[r.URL.Query().Get("n")] }

func errorFlash(text string) views.Flash { return views.Flash{Kind: "error", Text: text} }

// field reads a trimmed form value and reports whether it fits the limit.
func field(r *http.Request, name string, max int) (string, bool) {
	v := strings.TrimSpace(r.PostFormValue(name))
	v = strings.ReplaceAll(v, "\r\n", "\n")
	return v, utf8.RuneCountInString(v) <= max && utf8.ValidString(v)
}

// checked reads a switch. Its form sends a hidden "0" and, when ticked, a
// "1" after it, so an unticked box still arrives as an explicit "off".
func checked(r *http.Request, name string) bool {
	for _, v := range r.PostForm[name] {
		if v == "1" {
			return true
		}
	}
	return false
}

func pathID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

// --- organizer ---------------------------------------------------------------

func (s *Server) currentOrganizer(r *http.Request) (config.Organizer, bool) {
	c, err := r.Cookie(organizerCookie)
	if err != nil {
		return config.Organizer{}, false
	}
	return s.cfg.MatchOrganizer(c.Value)
}

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.currentOrganizer(r); ok {
		http.Redirect(w, r, "/organizer", http.StatusSeeOther)
		return
	}
	s.render(w, r, http.StatusOK, views.Home(views.Flash{}))
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	secret := strings.TrimSpace(r.PostFormValue("token"))
	ip := clientIP(r)
	if !s.logins.allow(ip) {
		s.log.Warn("organizer login throttled", "remote", ip)
		s.render(w, r, http.StatusTooManyRequests, views.Home(errorFlash("Zu viele Fehlversuche. Bitte versuche es in ein paar Minuten noch einmal.")))
		return
	}
	org, ok := s.cfg.MatchOrganizer(secret)
	if !ok {
		s.logins.fail(ip)
		s.log.Warn("organizer login failed", "remote", ip)
		s.render(w, r, http.StatusUnauthorized, views.Home(errorFlash("Dieser Schlüssel ist nicht bekannt.")))
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     organizerCookie,
		Value:    secret,
		Path:     "/",
		MaxAge:   int((30 * 24 * time.Hour).Seconds()),
		HttpOnly: true,
		Secure:   s.secure,
		SameSite: http.SameSiteLaxMode,
	})
	s.log.Info("organizer logged in", "organizer", org.Label)
	http.Redirect(w, r, "/organizer", http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: organizerCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) organizer(w http.ResponseWriter, r *http.Request) {
	org, ok := s.currentOrganizer(r)
	if !ok {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.renderOrganizer(w, r, org, http.StatusOK, noticeFrom(r), views.CampaignForm{})
}

func (s *Server) renderOrganizer(w http.ResponseWriter, r *http.Request, org config.Organizer, status int, f views.Flash, form views.CampaignForm) {
	campaigns, err := s.store.CampaignsByOrganizer(r.Context(), org.Label)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, status, views.Organizer(views.OrganizerPage{
		BaseURL: s.cfg.BaseURL, Label: org.Label, Campaigns: campaigns, Flash: f, Form: form,
	}))
}

func (s *Server) createCampaign(w http.ResponseWriter, r *http.Request) {
	org, ok := s.currentOrganizer(r)
	if !ok {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	title, okTitle := field(r, "title", maxTitle)
	budget, okBudget := field(r, "budget", maxBudget)
	date, _ := field(r, "draw_date", 10)
	form := views.CampaignForm{Title: title, Budget: budget, DrawDate: date}

	var problem string
	switch {
	case title == "":
		problem = "Bitte gib einen Titel an."
	case !okTitle || !okBudget:
		problem = "Titel oder Budget sind zu lang."
	case date != "" && !validDate(date):
		problem = "Das Datum der Auslosung ist ungültig."
	}
	if problem != "" {
		s.renderOrganizer(w, r, org, http.StatusUnprocessableEntity, errorFlash(problem), form)
		return
	}

	c, err := s.store.CreateCampaign(r.Context(), title, budget, date, org.Label)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.log.Info("campaign created", "campaign", c.ID, "organizer", org.Label)
	redirect(w, r, "/o/"+c.OrganizerToken, "created")
}

func validDate(v string) bool {
	_, err := time.Parse(time.DateOnly, v)
	return err == nil
}

// members builds the group overview. viewer is 0 on pages that belong to no
// participant.
func (s *Server) members(ctx context.Context, c store.Campaign, viewer int64) ([]views.Member, error) {
	ps, err := s.store.Participants(ctx, c.ID)
	if err != nil {
		return nil, err
	}
	counts, err := s.store.SurpriseCounts(ctx, c.ID)
	if err != nil {
		return nil, err
	}
	byMe := map[int64]bool{}
	if viewer != 0 {
		if byMe, err = s.store.SurprisesByAuthor(ctx, viewer); err != nil {
			return nil, err
		}
	}
	out := make([]views.Member, len(ps))
	for i, p := range ps {
		m := views.Member{Participant: p, IsMe: p.ID == viewer, ByMe: byMe[p.ID]}
		// Ideas for somebody who switched surprises off will never be shown,
		// so they do not count for anybody. Only the recipient is told how
		// many are stored, so they know what switching back on brings back.
		if p.SurprisesOpen {
			m.Surprises = counts[p.ID]
		} else if m.IsMe {
			m.Hidden = counts[p.ID]
		}
		out[i] = m
	}
	return out, nil
}

func (s *Server) manage(w http.ResponseWriter, r *http.Request) {
	c, err := s.store.CampaignByOrganizerToken(r.Context(), r.PathValue("token"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	members, err := s.members(r.Context(), c, 0)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	excl, err := s.store.Exclusions(r.Context(), c.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, views.Manage(views.ManagePage{
		CampaignPage: views.CampaignPage{Campaign: c, Members: members, Flash: noticeFrom(r)},
		JoinURL:      s.cfg.BaseURL + "/j/" + c.JoinToken,
		AdminURL:     views.OrganizerURL(s.cfg.BaseURL, c.OrganizerToken),
		Exclusions:   excl,
	}))
}

// organizerAction resolves the campaign behind an organizer link for a POST.
func (s *Server) organizerAction(w http.ResponseWriter, r *http.Request) (store.Campaign, bool) {
	c, err := s.store.CampaignByOrganizerToken(r.Context(), r.PathValue("token"))
	if err != nil {
		s.fail(w, r, err)
		return store.Campaign{}, false
	}
	return c, true
}

// afterChange redirects back with a notice matching the outcome of a store
// call, or fails the request if the error is not one users can cause.
func (s *Server) afterChange(w http.ResponseWriter, r *http.Request, back string, err error, ok string) {
	switch {
	case err == nil:
		redirect(w, r, back, ok)
	case errors.Is(err, store.ErrAlreadyDrawn):
		redirect(w, r, back, "drawn-already")
	case errors.Is(err, store.ErrNotReady):
		redirect(w, r, back, "not-ready")
	case errors.Is(err, store.ErrInvalid):
		redirect(w, r, back, "same-person")
	case errors.Is(err, store.ErrSurprisesClosed):
		redirect(w, r, back, "closed-already")
	case errors.Is(err, draw.ErrNoAssignment):
		redirect(w, r, back, "impossible")
	case errors.Is(err, draw.ErrTooFew):
		redirect(w, r, back, "too-few")
	default:
		s.fail(w, r, err)
	}
}

func (s *Server) deleteParticipant(w http.ResponseWriter, r *http.Request) {
	c, ok := s.organizerAction(w, r)
	if !ok {
		return
	}
	id, ok := pathID(r)
	if !ok {
		s.notFound(w, r)
		return
	}
	s.afterChange(w, r, "/o/"+c.OrganizerToken, s.store.DeleteParticipant(r.Context(), c.ID, id), "removed")
}

func (s *Server) addExclusion(w http.ResponseWriter, r *http.Request) {
	c, ok := s.organizerAction(w, r)
	if !ok {
		return
	}
	a, errA := strconv.ParseInt(r.PostFormValue("a"), 10, 64)
	b, errB := strconv.ParseInt(r.PostFormValue("b"), 10, 64)
	if errA != nil || errB != nil {
		redirect(w, r, "/o/"+c.OrganizerToken, "same-person")
		return
	}
	s.afterChange(w, r, "/o/"+c.OrganizerToken, s.store.AddExclusion(r.Context(), c.ID, a, b), "excluded")
}

func (s *Server) deleteExclusion(w http.ResponseWriter, r *http.Request) {
	c, ok := s.organizerAction(w, r)
	if !ok {
		return
	}
	id, ok := pathID(r)
	if !ok {
		s.notFound(w, r)
		return
	}
	s.afterChange(w, r, "/o/"+c.OrganizerToken, s.store.DeleteExclusion(r.Context(), c.ID, id), "unexcluded")
}

func (s *Server) draw(w http.ResponseWriter, r *http.Request) {
	c, ok := s.organizerAction(w, r)
	if !ok {
		return
	}
	err := s.store.Draw(r.Context(), c.ID)
	if err == nil {
		s.log.Info("campaign drawn", "campaign", c.ID)
	}
	s.afterChange(w, r, "/o/"+c.OrganizerToken, err, "drawn")
}

// --- joining -----------------------------------------------------------------

func (s *Server) joinPage(w http.ResponseWriter, r *http.Request) {
	c, err := s.store.CampaignByJoinToken(r.Context(), r.PathValue("token"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.renderJoin(w, r, c, http.StatusOK, views.Flash{}, "")
}

func (s *Server) renderJoin(w http.ResponseWriter, r *http.Request, c store.Campaign, status int, f views.Flash, name string) {
	members, err := s.members(r.Context(), c, 0)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, status, views.Join(views.JoinPage{
		CampaignPage: views.CampaignPage{Campaign: c, Members: members, Flash: f},
		Name:         name,
	}))
}

func (s *Server) join(w http.ResponseWriter, r *http.Request) {
	c, err := s.store.CampaignByJoinToken(r.Context(), r.PathValue("token"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	name, ok := field(r, "name", maxName)
	name = strings.Join(strings.Fields(name), " ")
	switch {
	case name == "":
		s.renderJoin(w, r, c, http.StatusUnprocessableEntity, errorFlash("Bitte gib deinen Namen an."), name)
		return
	case !ok:
		s.renderJoin(w, r, c, http.StatusUnprocessableEntity, errorFlash("Der Name ist zu lang."), name)
		return
	}

	p, err := s.store.AddParticipant(r.Context(), c.ID, name, "", "")
	switch {
	case errors.Is(err, store.ErrNameTaken):
		s.renderJoin(w, r, c, http.StatusConflict, errorFlash(
			"Den Namen „"+name+"“ gibt es schon. Falls du das bist: Öffne deinen persönlichen Link. Sonst wähle einen anderen Namen, z. B. mit Nachnamen."), name)
		return
	case errors.Is(err, store.ErrAlreadyDrawn):
		s.renderJoin(w, r, c, http.StatusConflict, notices["drawn-already"], name)
		return
	case err != nil:
		s.fail(w, r, err)
		return
	}
	s.log.Info("participant joined", "campaign", c.ID, "participant", p.ID)
	redirect(w, r, "/p/"+p.Token, "joined")
}

// --- participant -------------------------------------------------------------

// me resolves a participant link together with its campaign.
func (s *Server) me(w http.ResponseWriter, r *http.Request) (store.Participant, store.Campaign, bool) {
	p, err := s.store.ParticipantByToken(r.Context(), r.PathValue("token"))
	if err != nil {
		s.fail(w, r, err)
		return store.Participant{}, store.Campaign{}, false
	}
	c, err := s.store.CampaignByID(r.Context(), p.CampaignID)
	if err != nil {
		s.fail(w, r, err)
		return store.Participant{}, store.Campaign{}, false
	}
	return p, c, true
}

func (s *Server) participant(w http.ResponseWriter, r *http.Request) {
	me, c, ok := s.me(w, r)
	if !ok {
		return
	}
	s.renderParticipant(w, r, me, c, http.StatusOK, noticeFrom(r))
}

func (s *Server) renderParticipant(w http.ResponseWriter, r *http.Request, me store.Participant, c store.Campaign, status int, f views.Flash) {
	ctx := r.Context()
	members, err := s.members(ctx, c, me.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	page := views.ParticipantPage{
		CampaignPage: views.CampaignPage{Campaign: c, Members: members, Flash: f},
		Me:           me,
		MyURL:        s.cfg.BaseURL + "/p/" + me.Token,
	}
	if c.Drawn() {
		receiver, err := s.store.Receiver(ctx, c.ID, me.ID)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		page.Receiver = &receiver
		page.ReceiverDeclined = !receiver.SurprisesOpen
		if page.Surprises, err = s.store.SurprisesForGiver(ctx, c.ID, me.ID); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	s.render(w, r, status, views.Participant(page))
}

func (s *Server) saveWish(w http.ResponseWriter, r *http.Request) {
	me, c, ok := s.me(w, r)
	if !ok {
		return
	}
	text, ok := field(r, "wishlist", maxText)
	needsTime := checked(r, "needs_time")
	if !ok {
		// Show the form as submitted, switch included, so nothing the user
		// chose is lost when they shorten the text.
		me.Wishlist, me.NeedsTime = text, needsTime
		s.renderParticipant(w, r, me, c, http.StatusUnprocessableEntity, notices["too-long"])
		return
	}
	notice := "wish"
	switch {
	case text == "":
		// Time to think about no wish means nothing; dropping it keeps the
		// promise that entering a wish makes one ready.
		needsTime = false
		notice = "wish-cleared"
		if me.SurpriseSuffices {
			// The page itself says whether a surprise already makes them
			// ready; the notice must not claim they are not.
			notice = "wish-cleared-surprise"
		}
	case needsTime:
		notice = "wish-thinking"
	}
	s.afterChange(w, r, "/p/"+me.Token, s.store.SetWish(r.Context(), c.ID, me.ID, text, needsTime), notice)
}

func (s *Server) setSurprisesOpen(w http.ResponseWriter, r *http.Request) {
	me, c, ok := s.me(w, r)
	if !ok {
		return
	}
	open := checked(r, "open")
	// The second switch is only rendered while surprises are open. When
	// the form comes without it, keep what was stored; the store clears
	// it anyway on closing.
	suffices := me.SurpriseSuffices
	if _, sent := r.PostForm["suffices"]; sent {
		suffices = checked(r, "suffices")
	}
	var notice string
	switch {
	case !open && me.SurpriseSuffices:
		notice = "closed-suffices"
	case !open:
		notice = "closed"
	case suffices && !me.SurpriseSuffices:
		notice = "suffices"
	case !suffices && me.SurpriseSuffices:
		notice = "suffices-off"
	default:
		notice = "open"
	}
	s.afterChange(w, r, "/p/"+me.Token, s.store.SetSurprisePrefs(r.Context(), c.ID, me.ID, open, suffices), notice)
}

// recipient resolves the {id} of a surprise route to somebody else in the
// viewer's own campaign.
func (s *Server) recipient(w http.ResponseWriter, r *http.Request, me store.Participant) (store.Participant, bool) {
	id, ok := pathID(r)
	if !ok || id == me.ID {
		s.notFound(w, r)
		return store.Participant{}, false
	}
	p, err := s.store.ParticipantInCampaign(r.Context(), me.CampaignID, id)
	if err != nil {
		s.fail(w, r, err)
		return store.Participant{}, false
	}
	return p, true
}

func (s *Server) surprisePage(w http.ResponseWriter, r *http.Request) {
	me, c, ok := s.me(w, r)
	if !ok {
		return
	}
	rec, ok := s.recipient(w, r, me)
	if !ok {
		return
	}
	if c.Drawn() {
		redirect(w, r, "/p/"+me.Token, "drawn-already")
		return
	}
	existing, err := s.store.SurpriseBy(r.Context(), me.ID, rec.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.fail(w, r, err)
		return
	}
	exists := err == nil
	if !exists && !rec.SurprisesOpen {
		redirect(w, r, "/p/"+me.Token, "closed-already")
		return
	}
	s.render(w, r, http.StatusOK, views.SurpriseEdit(views.SurprisePage{
		Campaign: c, Me: me, Recipient: rec, Body: existing.Body, Exists: exists,
	}))
}

func (s *Server) saveSurprise(w http.ResponseWriter, r *http.Request) {
	me, c, ok := s.me(w, r)
	if !ok {
		return
	}
	rec, ok := s.recipient(w, r, me)
	if !ok {
		return
	}
	body, ok := field(r, "body", maxText)
	if r.PostFormValue("delete") == "1" {
		body, ok = "", true
	}
	if !ok {
		_, err := s.store.SurpriseBy(r.Context(), me.ID, rec.ID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			s.fail(w, r, err)
			return
		}
		s.render(w, r, http.StatusUnprocessableEntity, views.SurpriseEdit(views.SurprisePage{
			Campaign: c, Me: me, Recipient: rec, Body: body, Exists: err == nil, Flash: notices["too-long"],
		}))
		return
	}
	notice := "surprise"
	if body == "" {
		notice = "surprise-gone"
	}
	s.afterChange(w, r, "/p/"+me.Token, s.store.SaveSurprise(r.Context(), c.ID, me.ID, rec.ID, body), notice)
}

// preview renders Markdown for the editor's preview tab. It sits behind a
// participant link so it is not an open rendering service.
func (s *Server) preview(w http.ResponseWriter, r *http.Request) {
	if _, err := s.store.ParticipantByToken(r.Context(), r.PathValue("token")); err != nil {
		s.fail(w, r, err)
		return
	}
	text, ok := field(r, "text", maxText)
	if !ok {
		http.Error(w, "too long", http.StatusRequestEntityTooLarge)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if text == "" {
		w.Write([]byte(`<p class="muted">Noch nichts geschrieben.</p>`))
		return
	}
	w.Write([]byte(markdown.Render(text)))
}
