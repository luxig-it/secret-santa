package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func open(t *testing.T) *Store {
	t.Helper()
	return openAt(t, filepath.Join(t.TempDir(), "test.db"))
}

func openAt(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func mustCampaign(t *testing.T, s *Store) Campaign {
	t.Helper()
	c, err := s.CreateCampaign(context.Background(), "Office", "20 EUR", "2026-12-01", "carsten")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func mustJoin(t *testing.T, s *Store, c Campaign, name, wish string) Participant {
	t.Helper()
	p, err := s.AddParticipant(context.Background(), c.ID, name, "", wish)
	if err != nil {
		t.Fatalf("join %s: %v", name, err)
	}
	return p
}

func TestJoinRejectsDuplicateNames(t *testing.T) {
	s := open(t)
	c := mustCampaign(t, s)
	mustJoin(t, s, c, "Anna", "")
	mustJoin(t, s, c, "Jörg", "")
	for _, dup := range []string{"anna", " Anna ", "JÖRG"} {
		if _, err := s.AddParticipant(context.Background(), c.ID, dup, "", ""); !errors.Is(err, ErrNameTaken) {
			t.Fatalf("%q: got %v, want ErrNameTaken", dup, err)
		}
	}
	// The same name in a different campaign is fine.
	other := mustCampaign(t, s)
	mustJoin(t, s, other, "Anna", "")
}

func TestNewParticipantAcceptsSurprises(t *testing.T) {
	s := open(t)
	c := mustCampaign(t, s)
	p := mustJoin(t, s, c, "Anna", "")
	got, err := s.ParticipantByToken(context.Background(), p.Token)
	if err != nil {
		t.Fatal(err)
	}
	if !got.SurprisesOpen || got.HasWish() {
		t.Fatalf("got open=%v wish=%v, want open and no wish", got.SurprisesOpen, got.HasWish())
	}
}

func TestDrawNeedsEveryWish(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	c := mustCampaign(t, s)
	mustJoin(t, s, c, "Anna", "socks")
	mustJoin(t, s, c, "Ben", "book")
	carl := mustJoin(t, s, c, "Carl", "   \n")

	if err := s.Draw(ctx, c.ID); !errors.Is(err, ErrNotReady) {
		t.Fatalf("got %v, want ErrNotReady", err)
	}
	// The failed attempt must not have left the campaign half drawn.
	if got, _ := s.CampaignByID(ctx, c.ID); got.Drawn() {
		t.Fatal("campaign flagged as drawn after a refused draw")
	}

	if err := s.SetWish(ctx, c.ID, carl.ID, "tea", false); err != nil {
		t.Fatal(err)
	}
	if err := s.Draw(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Draw(ctx, c.ID); !errors.Is(err, ErrAlreadyDrawn) {
		t.Fatalf("second draw: got %v, want ErrAlreadyDrawn", err)
	}

	ps, _ := s.Participants(ctx, c.ID)
	seen := map[int64]bool{}
	for _, p := range ps {
		r, err := s.Receiver(ctx, c.ID, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		if r.ID == p.ID || seen[r.ID] {
			t.Fatalf("bad assignment for %s -> %s", p.Name, r.Name)
		}
		seen[r.ID] = true
	}

	if _, err := s.AddParticipant(ctx, c.ID, "Dora", "", ""); !errors.Is(err, ErrAlreadyDrawn) {
		t.Fatalf("late join: got %v, want ErrAlreadyDrawn", err)
	}
	if err := s.SetSurprisesOpen(ctx, c.ID, carl.ID, false); !errors.Is(err, ErrAlreadyDrawn) {
		t.Fatalf("switch after draw: got %v, want ErrAlreadyDrawn", err)
	}
}

func TestDrawUnsatisfiableLeavesCampaignOpen(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	c := mustCampaign(t, s)
	a := mustJoin(t, s, c, "Anna", "x")
	b := mustJoin(t, s, c, "Ben", "x")
	if err := s.AddExclusion(ctx, c.ID, a.ID, b.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Draw(ctx, c.ID); err == nil {
		t.Fatal("draw succeeded despite excluding the only possible pair")
	}
	if got, _ := s.CampaignByID(ctx, c.ID); got.Drawn() {
		t.Fatal("campaign flagged as drawn after a failed draw")
	}
}

func TestSurpriseRules(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	c := mustCampaign(t, s)
	anna := mustJoin(t, s, c, "Anna", "")
	ben := mustJoin(t, s, c, "Ben", "")
	carl := mustJoin(t, s, c, "Carl", "")
	stranger := mustJoin(t, s, mustCampaign(t, s), "Stranger", "")

	if err := s.SaveSurprise(ctx, c.ID, anna.ID, anna.ID, "me"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("self: got %v", err)
	}
	if err := s.SaveSurprise(ctx, c.ID, stranger.ID, anna.ID, "hi"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign author: got %v", err)
	}
	if err := s.SaveSurprise(ctx, c.ID, anna.ID, stranger.ID, "hi"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign recipient: got %v", err)
	}

	if err := s.SaveSurprise(ctx, c.ID, anna.ID, ben.ID, "a scarf"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSurprise(ctx, c.ID, anna.ID, ben.ID, "a red scarf"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSurprise(ctx, c.ID, carl.ID, ben.ID, "chocolate"); err != nil {
		t.Fatal(err)
	}

	got, err := s.SurpriseBy(ctx, anna.ID, ben.ID)
	if err != nil || got.Body != "a red scarf" || got.AuthorName != "Anna" {
		t.Fatalf("SurpriseBy = %+v, %v", got, err)
	}
	counts, _ := s.SurpriseCounts(ctx, c.ID)
	if counts[ben.ID] != 2 || counts[anna.ID] != 0 {
		t.Fatalf("counts = %v", counts)
	}
	mine, _ := s.SurprisesByAuthor(ctx, anna.ID)
	if !mine[ben.ID] || len(mine) != 1 {
		t.Fatalf("by author = %v", mine)
	}

	// Closing the switch blocks new ideas but keeps existing ones editable.
	if err := s.SetSurprisesOpen(ctx, c.ID, ben.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSurprise(ctx, c.ID, anna.ID, ben.ID, "a blue scarf"); err != nil {
		t.Fatalf("edit while closed: %v", err)
	}
	if err := s.SaveSurprise(ctx, c.ID, anna.ID, carl.ID, "x"); err != nil {
		t.Fatal(err) // Carl is still open.
	}
	if err := s.SetSurprisesOpen(ctx, c.ID, carl.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSurprise(ctx, c.ID, ben.ID, carl.ID, "new"); !errors.Is(err, ErrSurprisesClosed) {
		t.Fatalf("new while closed: got %v", err)
	}

	// An empty body removes the idea.
	if err := s.SaveSurprise(ctx, c.ID, carl.ID, ben.ID, "  "); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.SurpriseBy(ctx, anna.ID, ben.ID); got.Body != "a blue scarf" {
		t.Fatalf("edit while closed not stored: %+v", got)
	}
	if _, err := s.SurpriseBy(ctx, carl.ID, ben.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted surprise still found: %v", err)
	}

	// Removing a participant takes their ideas with them.
	if err := s.DeleteParticipant(ctx, c.ID, anna.ID); err != nil {
		t.Fatal(err)
	}
	if counts, _ := s.SurpriseCounts(ctx, c.ID); counts[ben.ID] != 0 {
		t.Fatalf("surprises of a deleted author survive: %v", counts)
	}
}

func TestSurprisesFrozenAfterDraw(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	c := mustCampaign(t, s)
	a := mustJoin(t, s, c, "Anna", "x")
	b := mustJoin(t, s, c, "Ben", "x")
	if err := s.SaveSurprise(ctx, c.ID, a.ID, b.ID, "for Ben"); err != nil {
		t.Fatal(err)
	}
	// Before the draw nobody, the author included, gets ideas this way.
	if got, _ := s.SurprisesForGiver(ctx, c.ID, a.ID); len(got) != 0 {
		t.Fatalf("ideas readable before the draw: %+v", got)
	}
	if err := s.Draw(ctx, c.ID); err != nil {
		t.Fatal(err)
	}

	// With two people Anna gives to Ben and Ben to Anna.
	got, err := s.SurprisesForGiver(ctx, c.ID, a.ID)
	if err != nil || len(got) != 1 || got[0].Body != "for Ben" || got[0].AuthorName != "Anna" {
		t.Fatalf("Anna's view = %+v, %v", got, err)
	}
	if got, _ := s.SurprisesForGiver(ctx, c.ID, b.ID); len(got) != 0 {
		t.Fatalf("Ben sees ideas left for himself: %+v", got)
	}

	if err := s.SaveSurprise(ctx, c.ID, a.ID, b.ID, "late"); !errors.Is(err, ErrAlreadyDrawn) {
		t.Fatalf("surprise after draw: got %v", err)
	}
	if err := s.SetWish(ctx, c.ID, a.ID, "", false); !errors.Is(err, ErrAlreadyDrawn) {
		t.Fatalf("wish after draw: got %v", err)
	}
	if err := s.AddExclusion(ctx, c.ID, a.ID, b.ID); !errors.Is(err, ErrAlreadyDrawn) {
		t.Fatalf("exclusion after draw: got %v", err)
	}
	if err := s.DeleteParticipant(ctx, c.ID, a.ID); !errors.Is(err, ErrAlreadyDrawn) {
		t.Fatalf("delete after draw: got %v", err)
	}
}

func TestUpdatesStayInsideTheirCampaign(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	c := mustCampaign(t, s)
	other := mustCampaign(t, s)
	p := mustJoin(t, s, other, "Anna", "")
	if err := s.SetWish(ctx, c.ID, p.ID, "x", false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wish: got %v", err)
	}
	if err := s.SetSurprisesOpen(ctx, c.ID, p.ID, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("switch: got %v", err)
	}
	if err := s.DeleteParticipant(ctx, c.ID, p.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete: got %v", err)
	}
}

func TestCampaignsByOrganizer(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	first := mustCampaign(t, s)
	second := mustCampaign(t, s)
	if _, err := s.CreateCampaign(ctx, "Other", "", "", "anna"); err != nil {
		t.Fatal(err)
	}
	got, err := s.CampaignsByOrganizer(ctx, "carsten")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != second.ID || got[1].ID != first.ID {
		t.Fatalf("got %+v", got)
	}
}

func TestNeedsTimeBlocksDraw(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	c := mustCampaign(t, s)
	mustJoin(t, s, c, "Anna", "x")
	ben := mustJoin(t, s, c, "Ben", "")

	if err := s.SetWish(ctx, c.ID, ben.ID, "book", true); err != nil {
		t.Fatal(err)
	}
	got, _ := s.ParticipantByToken(ctx, ben.Token)
	if !got.HasWish() || !got.NeedsTime || got.Ready() {
		t.Fatalf("wish=%v needsTime=%v ready=%v, want wish, time, not ready", got.HasWish(), got.NeedsTime, got.Ready())
	}
	if err := s.Draw(ctx, c.ID); !errors.Is(err, ErrNotReady) {
		t.Fatalf("draw while Ben thinks: got %v", err)
	}
	if err := s.SetWish(ctx, c.ID, ben.ID, "book", false); err != nil {
		t.Fatal(err)
	}
	if err := s.Draw(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationAddsColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	s := openAt(t, path)
	// Simulate a database from before needs_time existed.
	if _, err := s.db.Exec(`ALTER TABLE participant DROP COLUMN needs_time`); err != nil {
		t.Fatal(err)
	}
	c := mustCampaign(t, s)
	if _, err := s.db.Exec(`INSERT INTO participant (campaign_id, name, token, created_at) VALUES (?, 'Old', 'old-token', '2026-01-01T00:00:00Z')`, c.ID); err != nil {
		t.Fatal(err)
	}
	s.Close() // the Cleanup's second Close is harmless

	s = openAt(t, path)
	p, err := s.ParticipantByToken(context.Background(), "old-token")
	if err != nil {
		t.Fatal(err)
	}
	if p.NeedsTime {
		t.Fatal("migrated participant needs time by default")
	}
}
