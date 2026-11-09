// Package config reads the whole configuration from the environment. There is
// no config file on purpose: a single container with a handful of variables is
// the entire operational surface of this app.
package config

import (
	"fmt"
	"net/mail"
	"net/netip"
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

func (s SMTP) check() error {
	if !s.Enabled() {
		return fmt.Errorf("SECRET_SANTA_ALLOW_EMAIL needs SECRET_SANTA_SMTP_HOST and SECRET_SANTA_SMTP_FROM")
	}
	if _, err := mail.ParseAddress(s.From); err != nil {
		return fmt.Errorf("SECRET_SANTA_SMTP_FROM %q is not an address like \"Wichteln <wichteln@example.com>\": %w", s.From, err)
	}
	if s.Port == "465" {
		return fmt.Errorf("SECRET_SANTA_SMTP_PORT 465 (implicit TLS) is not supported; use the STARTTLS port, usually 587")
	}
	if (s.User == "") != (s.Pass == "") {
		return fmt.Errorf("set both SECRET_SANTA_SMTP_USER and SECRET_SANTA_SMTP_PASS, or neither")
	}
	return nil
}

// Addr is the host:port net/smtp expects.
func (s SMTP) Addr() string { return s.Host + ":" + s.Port }

// Config is the fully resolved configuration.
type Config struct {
	Addr       string
	BaseURL    string
	DBPath     string
	Organizers []Organizer
	SMTP       SMTP
	// SMTPInsecure allows sending without STARTTLS, for a local relay.
	SMTPInsecure bool
	// TrustedProxies are the addresses of reverse proxies whose X-Real-IP
	// header names the actual client. Requests from anywhere else are taken
	// at their connection address, so nobody can fake their IP.
	TrustedProxies []netip.Prefix
	// AllowEmail offers sending personal and management links by mail.
	// It requires a complete SMTP configuration.
	AllowEmail bool

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
	allow, err := parseBool(env("SECRET_SANTA_ALLOW_EMAIL", "false"))
	if err != nil {
		return Config{}, fmt.Errorf("SECRET_SANTA_ALLOW_EMAIL: %w", err)
	}
	c.AllowEmail = allow
	insecure, err := parseBool(env("SECRET_SANTA_SMTP_INSECURE", "false"))
	if err != nil {
		return Config{}, fmt.Errorf("SECRET_SANTA_SMTP_INSECURE: %w", err)
	}
	c.SMTPInsecure = insecure

	orgs, err := ParseOrganizers(env("SECRET_SANTA_ORGANIZER_TOKENS", ""))
	if err != nil {
		return Config{}, err
	}
	if strings.HasPrefix(c.BaseURL, "https://") {
		// A public instance must not run with a key printed in this
		// repository's examples.
		for _, o := range orgs {
			if isPublicExampleToken(o.Token) {
				return Config{}, fmt.Errorf("organizer %q still uses the example key %q; set a long random key (e.g. openssl rand -base64 24)", o.Label, o.Token)
			}
		}
	}
	proxies, err := ParseProxies(env("SECRET_SANTA_TRUSTED_PROXY", ""))
	if err != nil {
		return Config{}, fmt.Errorf("SECRET_SANTA_TRUSTED_PROXY: %w", err)
	}
	c.TrustedProxies = proxies
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
	if c.AllowEmail {
		// Failing at start beats a button that fails on every click. Only
		// checked when mail is on, so settings kept for later do no harm.
		if err := c.SMTP.check(); err != nil {
			return Config{}, err
		}
	}
	return c, nil
}

// ParseOrganizers reads the "label:token,label:token" form. A bare token
// without a label is accepted and gets a generic one, numbered if needed.
//
// Labels must be unique: campaigns are filed under their organizer's label,
// so two organizers sharing one would see each other's campaigns. Renaming a
// label therefore also detaches the campaigns filed under the old name.
func ParseOrganizers(raw string) ([]Organizer, error) {
	var out []Organizer
	seen := map[string]bool{}
	labels := map[string]bool{}
	bare := 0
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		var label, tok string
		if i := strings.LastIndex(part, ":"); i >= 0 {
			label, tok = strings.TrimSpace(part[:i]), strings.TrimSpace(part[i+1:])
		} else {
			bare++
			label, tok = "organizer", part
			if bare > 1 {
				label = fmt.Sprintf("organizer%d", bare)
			}
		}
		if label == "" {
			return nil, fmt.Errorf("an organizer entry has an empty label")
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
		if labels[label] {
			return nil, fmt.Errorf("organizer label %q is used twice", label)
		}
		seen[tok] = true
		labels[label] = true
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

func parseBool(v string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true, nil
	case "", "0", "false", "no", "off":
		return false, nil
	}
	return false, fmt.Errorf("%q is not a yes/no value", v)
}

// publicExampleTokens appear in this repository's examples and docs, so
// anybody knows them.
var publicExampleTokens = []string{
	"change-me", "change-me-too", "dev-organizer-token", "a-long-secret",
	"REPLACE-WITH-A-LONG-RANDOM-KEY",
}

func isPublicExampleToken(t string) bool {
	for _, p := range publicExampleTokens {
		if strings.EqualFold(t, p) {
			return true
		}
	}
	return strings.Contains(strings.ToUpper(t), "REPLACE")
}

// ParseProxies reads a comma separated list of IP addresses and CIDR ranges.
func ParseProxies(raw string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if p, err := netip.ParsePrefix(part); err == nil {
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(part)
		if err != nil {
			return nil, fmt.Errorf("%q is neither an IP address nor a CIDR range", part)
		}
		out = append(out, netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()))
	}
	return out, nil
}
