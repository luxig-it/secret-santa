package token

import "testing"

func TestNewIsURLSafeAndUnique(t *testing.T) {
	seen := map[string]bool{}
	for range 1000 {
		tok := New()
		if len(tok) != 22 {
			t.Fatalf("token %q has length %d, want 22", tok, len(tok))
		}
		for _, r := range tok {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
				t.Fatalf("token %q contains %q", tok, r)
			}
		}
		if seen[tok] {
			t.Fatalf("duplicate token %q", tok)
		}
		seen[tok] = true
	}
}

func TestEqual(t *testing.T) {
	if !Equal("abc", "abc") || Equal("abc", "abd") || Equal("abc", "ab") || Equal("", "a") {
		t.Fatal("Equal gives wrong answers")
	}
}
