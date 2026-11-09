// Package config reads the whole configuration from the environment. There is
// no config file on purpose: a single container with a handful of variables is
// the entire operational surface of this app.
package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/luxig-it/secret-santa/internal/token"
)

// Organizer is someone allowed to create campaigns. The label is cosmetic; the
// token is the shared secret they type into the start page.
type Organizer struct {
	Label string
	Token string
}

// SMTP holds the optional mail credentials.
type SMTP struct {
	Host string
	Port string
	User string
	Pass string
	From string
}

// Enabled reports whether enough is configured to actually send mail.
func (s SMTP) Enabled() bool { return s.Host != "" && s.From != "" }

// Addr is the host:port net/smtp expects.
func (s SMTP) Addr() string { return s.Host + ":" + s.Port }

// Config is the fully resolved configuration.
type Config struct {
	Addr       string
	BaseURL    string
	DBPath     string
	Organizers []Organizer
	SMTP       SMTP

	// GeneratedToken is set when no organizer token was configured and one was
	// invented on the spot, so main can print it once.
	GeneratedToken string
}

// Load reads the environment and fills in defaults.
func Load() (Config, error) {
	c := Config{
		Addr:    env("SECRET_SANTA_ADDR", ":8080"),
		BaseURL: strings.TrimRight(env("SECRET_SANTA_BASE_URL", "http://localhost:8080"), "/"),
		DBPath:  env("SECRET_SANTA_DB", "/data/secret-santa.db"),
		SMTP: SMTP{
			Host: env("SECRET_SANTA_SMTP_HOST", ""),
			Port: env("SECRET_SANTA_SMTP_PORT", "587"),
			User: env("SECRET_SANTA_SMTP_USER", ""),
			Pass: env("SECRET_SANTA_SMTP_PASS", ""),
			From: env("SECRET_SANTA_SMTP_FROM", ""),
		},
	}

	orgs, err := ParseOrganizers(env("SECRET_SANTA_ORGANIZER_TOKENS", ""))
	if err != nil {
		return Config{}, err
	}
	if len(orgs) == 0 {
		// Running with no configuration at all should still work, otherwise
		// nobody ever gets as far as trying the app out.
		t := token.New()
		orgs = []Organizer{{Label: "organizer", Token: t}}
		c.GeneratedToken = t
	}
	c.Organizers = orgs

	if c.SMTP.Host != "" && c.SMTP.From == "" {
		return Config{}, fmt.Errorf("SECRET_SANTA_SMTP_FROM is required when SECRET_SANTA_SMTP_HOST is set")
	}
	return c, nil
}

// ParseOrganizers reads the "label:token,label:token" form. A bare token
// without a label is accepted and gets a generic one.
func ParseOrganizers(raw string) ([]Organizer, error) {
	var out []Organizer
	seen := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		label, tok := "organizer", part
		if i := strings.LastIndex(part, ":"); i >= 0 {
			label, tok = strings.TrimSpace(part[:i]), strings.TrimSpace(part[i+1:])
		}
		if tok == "" {
			return nil, fmt.Errorf("organizer %q has an empty token", label)
		}
		if len(tok) < 8 {
			return nil, fmt.Errorf("organizer %q: token must be at least 8 characters", label)
		}
		if seen[tok] {
			return nil, fmt.Errorf("organizer %q reuses a token that is already taken", label)
		}
		seen[tok] = true
		out = append(out, Organizer{Label: label, Token: tok})
	}
	return out, nil
}

// MatchOrganizer returns the organizer owning the given secret, comparing in
// constant time.
func (c Config) MatchOrganizer(secret string) (Organizer, bool) {
	var found Organizer
	ok := false
	// Walk every entry regardless of an early hit so the duration does not
	// depend on the position of the match.
	for _, o := range c.Organizers {
		if token.Equal(o.Token, secret) {
			found, ok = o, true
		}
	}
	return found, ok
}

func env(key, def string) string {
	if v, present := os.LookupEnv(key); present && v != "" {
		return v
	}
	return def
}
