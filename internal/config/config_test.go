package config

import "testing"

func TestParseOrganizers(t *testing.T) {
	orgs, err := ParseOrganizers(" carsten:s3cr3t-token , anna:h0h0h0-token ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(orgs) != 2 {
		t.Fatalf("got %d organizers, want 2", len(orgs))
	}
	if orgs[0].Label != "carsten" || orgs[0].Token != "s3cr3t-token" {
		t.Errorf("first entry parsed as %+v", orgs[0])
	}
	if orgs[1].Label != "anna" {
		t.Errorf("second label is %q", orgs[1].Label)
	}
}

func TestParseOrganizersBareToken(t *testing.T) {
	orgs, err := ParseOrganizers("just-a-long-token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(orgs) != 1 || orgs[0].Label != "organizer" {
		t.Fatalf("got %+v", orgs)
	}
}

func TestParseOrganizersNumbersBareTokens(t *testing.T) {
	orgs, err := ParseOrganizers("first-long-token,second-long-token")
	if err != nil {
		t.Fatal(err)
	}
	if orgs[0].Label == orgs[1].Label {
		t.Fatalf("bare tokens share the label %q", orgs[0].Label)
	}
}

func TestParseOrganizersRejects(t *testing.T) {
	for name, raw := range map[string]string{
		"short token":     "carsten:abc",
		"empty token":     "carsten:",
		"duplicate token": "a:same-token-here,b:same-token-here",
		"duplicate label": "carsten:first-token-x,carsten:second-token",
		"empty label":     ":some-long-token",
	} {
		if _, err := ParseOrganizers(raw); err == nil {
			t.Errorf("%s: expected an error for %q", name, raw)
		}
	}
}

func TestMatchOrganizer(t *testing.T) {
	c := Config{Organizers: []Organizer{{Label: "carsten", Token: "s3cr3t-token"}}}
	if o, ok := c.MatchOrganizer("s3cr3t-token"); !ok || o.Label != "carsten" {
		t.Errorf("valid secret did not match: %+v %v", o, ok)
	}
	if _, ok := c.MatchOrganizer("nope-nope-nope"); ok {
		t.Error("wrong secret matched")
	}
	if _, ok := c.MatchOrganizer(""); ok {
		t.Error("empty secret matched")
	}
}

func TestLoadEmail(t *testing.T) {
	base := map[string]string{"SECRET_SANTA_ORGANIZER_TOKENS": "a:long-enough-token"}
	cases := []struct {
		name    string
		env     map[string]string
		wantErr bool
		allow   bool
	}{
		{"off by default", nil, false, false},
		{"on with smtp", map[string]string{"SECRET_SANTA_ALLOW_EMAIL": "true", "SECRET_SANTA_SMTP_HOST": "smtp.example.com", "SECRET_SANTA_SMTP_FROM": "x@example.com"}, false, true},
		{"on without smtp", map[string]string{"SECRET_SANTA_ALLOW_EMAIL": "1"}, true, false},
		{"garbage flag", map[string]string{"SECRET_SANTA_ALLOW_EMAIL": "maybe"}, true, false},
		{"implicit tls port", map[string]string{"SECRET_SANTA_ALLOW_EMAIL": "on", "SECRET_SANTA_SMTP_HOST": "h", "SECRET_SANTA_SMTP_FROM": "x@example.com", "SECRET_SANTA_SMTP_PORT": "465"}, false, true},
		{"user without pass", map[string]string{"SECRET_SANTA_ALLOW_EMAIL": "on", "SECRET_SANTA_SMTP_HOST": "h", "SECRET_SANTA_SMTP_FROM": "x@example.com", "SECRET_SANTA_SMTP_USER": "u"}, true, false},
		{"port with spaces", map[string]string{"SECRET_SANTA_ALLOW_EMAIL": "on", "SECRET_SANTA_SMTP_HOST": "h", "SECRET_SANTA_SMTP_FROM": "x@example.com", "SECRET_SANTA_SMTP_PORT": " 465 "}, false, true},
		{"port not a number", map[string]string{"SECRET_SANTA_ALLOW_EMAIL": "on", "SECRET_SANTA_SMTP_HOST": "h", "SECRET_SANTA_SMTP_FROM": "x@example.com", "SECRET_SANTA_SMTP_PORT": "smtps"}, true, false},
		{"bad tls mode", map[string]string{"SECRET_SANTA_ALLOW_EMAIL": "on", "SECRET_SANTA_SMTP_HOST": "h", "SECRET_SANTA_SMTP_FROM": "x@example.com", "SECRET_SANTA_SMTP_TLS": "ssl"}, true, false},
		{"malformed sender", map[string]string{"SECRET_SANTA_ALLOW_EMAIL": "on", "SECRET_SANTA_SMTP_HOST": "h", "SECRET_SANTA_SMTP_FROM": "Wichteln wichteln@example.com"}, true, false},
		{"odd settings are fine while mail is off", map[string]string{"SECRET_SANTA_SMTP_HOST": "h", "SECRET_SANTA_SMTP_FROM": "x@example.com", "SECRET_SANTA_SMTP_PORT": "465"}, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for _, k := range []string{"SECRET_SANTA_ALLOW_EMAIL", "SECRET_SANTA_SMTP_INSECURE", "SECRET_SANTA_SMTP_TLS", "SECRET_SANTA_SMTP_HOST", "SECRET_SANTA_SMTP_FROM", "SECRET_SANTA_SMTP_PORT", "SECRET_SANTA_SMTP_USER", "SECRET_SANTA_SMTP_PASS"} {
				t.Setenv(k, "")
			}
			for k, v := range base {
				t.Setenv(k, v)
			}
			for k, v := range c.env {
				t.Setenv(k, v)
			}
			cfg, err := Load()
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, want error %v", err, c.wantErr)
			}
			if err == nil && cfg.AllowEmail != c.allow {
				t.Fatalf("AllowEmail = %v, want %v", cfg.AllowEmail, c.allow)
			}
		})
	}
}

func TestPublicExampleKeysRejectedOnHTTPS(t *testing.T) {
	for _, tc := range []struct {
		base, tokens string
		wantErr      bool
	}{
		{"https://wichteln.example.com", "carsten:REPLACE-WITH-A-LONG-RANDOM-KEY", true},
		{"https://wichteln.example.com", "carsten:change-me,anna:x8f2kQ9zLpW3vN7a", true},
		{"https://wichteln.example.com", "carsten:x8f2kQ9zLpW3vN7a", false},
		{"http://localhost:8080", "dev:dev-organizer-token", false},
	} {
		t.Setenv("SECRET_SANTA_BASE_URL", tc.base)
		t.Setenv("SECRET_SANTA_ORGANIZER_TOKENS", tc.tokens)
		if _, err := Load(); (err != nil) != tc.wantErr {
			t.Errorf("%s %s: err = %v, want error %v", tc.base, tc.tokens, err, tc.wantErr)
		}
	}
}

func TestParseProxies(t *testing.T) {
	got, err := ParseProxies(" 172.16.0.0/12, 127.0.0.1 ,::1")
	if err != nil || len(got) != 3 || got[0].String() != "172.16.0.0/12" || got[1].String() != "127.0.0.1/32" {
		t.Fatalf("got %v, %v", got, err)
	}
	if _, err := ParseProxies("nginx"); err == nil {
		t.Fatal("a hostname was accepted")
	}
}

func TestSMTPTLSMode(t *testing.T) {
	cases := []struct {
		port, mode string
		implicit   bool
	}{
		{"465", "", true},
		{"587", "", false},
		{"2465", "implicit", true},
		{"465", "starttls", false},
	}
	for _, c := range cases {
		t.Setenv("SECRET_SANTA_ORGANIZER_TOKENS", "a:long-enough-token")
		t.Setenv("SECRET_SANTA_SMTP_PORT", c.port)
		t.Setenv("SECRET_SANTA_SMTP_TLS", c.mode)
		cfg, err := Load()
		if err != nil || cfg.SMTP.ImplicitTLS != c.implicit {
			t.Errorf("port %s mode %q: implicit=%v err=%v, want %v", c.port, c.mode, cfg.SMTP.ImplicitTLS, err, c.implicit)
		}
	}
}
