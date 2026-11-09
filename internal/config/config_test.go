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

func TestParseOrganizersRejects(t *testing.T) {
	for name, raw := range map[string]string{
		"short token":     "carsten:abc",
		"empty token":     "carsten:",
		"duplicate token": "a:same-token-here,b:same-token-here",
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
