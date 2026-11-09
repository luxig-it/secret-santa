// Package store is the entire persistence layer: a single SQLite file holding
// campaigns, participants, exclusions and the finished draw.
//
// It uses modernc.org/sqlite, a pure Go implementation, so the binary needs no
// CGO and no shared libraries and cross compiles to any platform Go supports.
package store

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/luxig-it/secret-santa/internal/draw"
	"github.com/luxig-it/secret-santa/internal/token"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schema string

// ErrNotFound is returned when a token matches nothing. Handlers turn it into
// a 404 without distinguishing "wrong token" from "deleted campaign", so a
// probe learns nothing either way.
var ErrNotFound = errors.New("not found")

// ErrAlreadyDrawn guards the operations that only make sense before the draw.
var ErrAlreadyDrawn = errors.New("this campaign has already been drawn")

// Campaign states.
const (
	StateOpen  = "open"
	StateDrawn = "drawn"
)

// Campaign is one Secret Santa round.
type Campaign struct {
	ID             int64
	Title          string
	Budget         string
	DrawDate       string
	JoinToken      string
	OrganizerToken string
	OrganizerLabel string
	State          string
	CreatedAt      time.Time
}

// Drawn reports whether the assignment has been made.
func (c Campaign) Drawn() bool { return c.State == StateDrawn }

// Participant is one person in a campaign. The token is their private link.
type Participant struct {
	ID         int64
	CampaignID int64
	Name       string
	Email      string
	Wishlist   string
	Ready      bool
	Token      string
	CreatedAt  time.Time
}

// Exclusion is a pair who must not draw each other, carrying the names so the
// organizer view does not need a second lookup.
type Exclusion struct {
	ID    int64
	AID   int64
	BID   int64
	AName string
	BName string
}

// Store owns the database handle.
type Store struct {
	db *sql.DB
}

// Open opens (and if needed creates) the database at path and applies the
// schema. Missing parent directories are created.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
	}

	// WAL keeps readers from blocking the writer, and busy_timeout turns the
	// rare write collision into a short wait instead of an error.
	dsn := "file:" + url.PathEscape(path) +
		"?_pragma=busy_timeout(5000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=foreign_keys(1)"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("reach database at %s: %w", path, err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

const campaignColumns = `id, title, budget, draw_date, join_token,
	organizer_token, organizer_label, state, created_at`

func scanCampaign(row interface{ Scan(...any) error }) (Campaign, error) {
	var c Campaign
	var created string
	err := row.Scan(&c.ID, &c.Title, &c.Budget, &c.DrawDate, &c.JoinToken,
		&c.OrganizerToken, &c.OrganizerLabel, &c.State, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return Campaign{}, ErrNotFound
	}
	if err != nil {
		return Campaign{}, err
	}
	c.CreatedAt, _ = time.Parse(time.RFC3339, created)
	return c, nil
}

// CreateCampaign stores a new campaign with freshly generated tokens.
func (s *Store) CreateCampaign(ctx context.Context, title, budget, drawDate, organizerLabel string) (Campaign, error) {
	c := Campaign{
		Title:          title,
		Budget:         budget,
		DrawDate:       drawDate,
		JoinToken:      token.New(),
		OrganizerToken: token.New(),
		OrganizerLabel: organizerLabel,
		State:          StateOpen,
		CreatedAt:      time.Now().UTC(),
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO campaign (title, budget, draw_date, join_token,
			organizer_token, organizer_label, state, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		c.Title, c.Budget, c.DrawDate, c.JoinToken,
		c.OrganizerToken, c.OrganizerLabel, c.State, c.CreatedAt.Format(time.RFC3339))
	if err != nil {
		return Campaign{}, fmt.Errorf("insert campaign: %w", err)
	}
	if c.ID, err = res.LastInsertId(); err != nil {
		return Campaign{}, err
	}
	return c, nil
}

// CampaignByJoinToken looks up a campaign by its public join link.
func (s *Store) CampaignByJoinToken(ctx context.Context, t string) (Campaign, error) {
	return scanCampaign(s.db.QueryRowContext(ctx,
		`SELECT `+campaignColumns+` FROM campaign WHERE join_token = ?`, t))
}

// CampaignByOrganizerToken looks up a campaign by its management link.
func (s *Store) CampaignByOrganizerToken(ctx context.Context, t string) (Campaign, error) {
	return scanCampaign(s.db.QueryRowContext(ctx,
		`SELECT `+campaignColumns+` FROM campaign WHERE organizer_token = ?`, t))
}

// CampaignByID looks up a campaign by its primary key.
func (s *Store) CampaignByID(ctx context.Context, id int64) (Campaign, error) {
	return scanCampaign(s.db.QueryRowContext(ctx,
		`SELECT `+campaignColumns+` FROM campaign WHERE id = ?`, id))
}

const participantColumns = `id, campaign_id, name, email, wishlist, ready, token, created_at`

func scanParticipant(row interface{ Scan(...any) error }) (Participant, error) {
	var p Participant
	var created string
	err := row.Scan(&p.ID, &p.CampaignID, &p.Name, &p.Email, &p.Wishlist,
		&p.Ready, &p.Token, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return Participant{}, ErrNotFound
	}
	if err != nil {
		return Participant{}, err
	}
	p.CreatedAt, _ = time.Parse(time.RFC3339, created)
	return p, nil
}

// AddParticipant joins somebody to a campaign that is still open.
func (s *Store) AddParticipant(ctx context.Context, campaignID int64, name, email, wishlist string) (Participant, error) {
	c, err := s.CampaignByID(ctx, campaignID)
	if err != nil {
		return Participant{}, err
	}
	if c.Drawn() {
		return Participant{}, ErrAlreadyDrawn
	}

	p := Participant{
		CampaignID: campaignID,
		Name:       name,
		Email:      email,
		Wishlist:   wishlist,
		Token:      token.New(),
		CreatedAt:  time.Now().UTC(),
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO participant (campaign_id, name, email, wishlist, ready, token, created_at)
		 VALUES (?, ?, ?, ?, 0, ?, ?)`,
		p.CampaignID, p.Name, p.Email, p.Wishlist, p.Token, p.CreatedAt.Format(time.RFC3339))
	if err != nil {
		return Participant{}, fmt.Errorf("insert participant: %w", err)
	}
	if p.ID, err = res.LastInsertId(); err != nil {
		return Participant{}, err
	}
	return p, nil
}

// ParticipantByToken resolves somebody's private link.
func (s *Store) ParticipantByToken(ctx context.Context, t string) (Participant, error) {
	return scanParticipant(s.db.QueryRowContext(ctx,
		`SELECT `+participantColumns+` FROM participant WHERE token = ?`, t))
}

// Participants lists a campaign in join order.
func (s *Store) Participants(ctx context.Context, campaignID int64) ([]Participant, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+participantColumns+` FROM participant
		 WHERE campaign_id = ? ORDER BY id`, campaignID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Participant
	for rows.Next() {
		p, err := scanParticipant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SetWishlist replaces somebody's wish list text.
func (s *Store) SetWishlist(ctx context.Context, participantID int64, text string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE participant SET wishlist = ? WHERE id = ?`, text, participantID)
	return err
}

// SetReady flags somebody as done entering their wishes.
func (s *Store) SetReady(ctx context.Context, participantID int64, ready bool) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE participant SET ready = ? WHERE id = ?`, ready, participantID)
	return err
}

// DeleteParticipant removes somebody from a campaign that is still open. The
// campaign id is part of the condition so an organizer token cannot be used to
// reach into a different campaign.
func (s *Store) DeleteParticipant(ctx context.Context, campaignID, participantID int64) error {
	c, err := s.CampaignByID(ctx, campaignID)
	if err != nil {
		return err
	}
	if c.Drawn() {
		return ErrAlreadyDrawn
	}
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM participant WHERE id = ? AND campaign_id = ?`, participantID, campaignID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Exclusions lists the pairs who must not draw each other.
func (s *Store) Exclusions(ctx context.Context, campaignID int64) ([]Exclusion, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT e.id, e.a_id, e.b_id, a.name, b.name
		 FROM exclusion e
		 JOIN participant a ON a.id = e.a_id
		 JOIN participant b ON b.id = e.b_id
		 WHERE e.campaign_id = ?
		 ORDER BY a.name, b.name`, campaignID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Exclusion
	for rows.Next() {
		var e Exclusion
		if err := rows.Scan(&e.ID, &e.AID, &e.BID, &e.AName, &e.BName); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// AddExclusion records that two people must not draw each other. Both must
// belong to the campaign, and the pair is normalised so it cannot be stored
// twice in opposite order.
func (s *Store) AddExclusion(ctx context.Context, campaignID, aID, bID int64) error {
	if aID == bID {
		return errors.New("an exclusion needs two different people")
	}
	if aID > bID {
		aID, bID = bID, aID
	}
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM participant WHERE campaign_id = ? AND id IN (?, ?)`,
		campaignID, aID, bID).Scan(&n); err != nil {
		return err
	}
	if n != 2 {
		return ErrNotFound
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO exclusion (campaign_id, a_id, b_id) VALUES (?, ?, ?)`,
		campaignID, aID, bID)
	return err
}

// DeleteExclusion drops a pair again.
func (s *Store) DeleteExclusion(ctx context.Context, campaignID, exclusionID int64) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM exclusion WHERE id = ? AND campaign_id = ?`, exclusionID, campaignID)
	return err
}

// Draw performs the draw and stores it, all in one transaction so a campaign
// can never end up flagged as drawn without a complete assignment. It fails if
// the campaign was already drawn, which also makes a double submit harmless.
func (s *Store) Draw(ctx context.Context, campaignID int64) error {
	participants, err := s.Participants(ctx, campaignID)
	if err != nil {
		return err
	}
	exclusions, err := s.Exclusions(ctx, campaignID)
	if err != nil {
		return err
	}

	ids := make([]int64, len(participants))
	for i, p := range participants {
		ids[i] = p.ID
	}
	pairs := make([]draw.Pair, len(exclusions))
	for i, e := range exclusions {
		pairs[i] = draw.Pair{A: e.AID, B: e.BID}
	}

	assignment, err := draw.Assign(ids, pairs)
	if err != nil {
		return err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Claiming the state transition first means a concurrent second draw finds
	// no open campaign and stops here.
	res, err := tx.ExecContext(ctx,
		`UPDATE campaign SET state = ? WHERE id = ? AND state = ?`,
		StateDrawn, campaignID, StateOpen)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrAlreadyDrawn
	}

	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO assignment (campaign_id, giver_id, receiver_id) VALUES (?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for giver, receiver := range assignment {
		if _, err := stmt.ExecContext(ctx, campaignID, giver, receiver); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Receiver returns the person a giver has to find a present for.
func (s *Store) Receiver(ctx context.Context, campaignID, giverID int64) (Participant, error) {
	return scanParticipant(s.db.QueryRowContext(ctx,
		`SELECT `+participantColumns+` FROM participant
		 WHERE id = (SELECT receiver_id FROM assignment
		             WHERE campaign_id = ? AND giver_id = ?)`,
		campaignID, giverID))
}
