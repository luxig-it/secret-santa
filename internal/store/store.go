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
	"strings"
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

// ErrNotReady means at least one participant has not entered a wish yet, so
// the organizer cannot draw.
var ErrNotReady = errors.New("not every participant has entered a wish")

// ErrNameTaken means somebody with the same name already joined, which would
// make the group list ambiguous.
var ErrNameTaken = errors.New("a participant with this name already exists")

// ErrSurprisesClosed means the recipient does not accept surprise ideas.
var ErrSurprisesClosed = errors.New("this participant does not accept surprises")

// ErrInvalid covers requests that make no sense, such as leaving a surprise
// for oneself or an exclusion of a person with themselves.
var ErrInvalid = errors.New("invalid request")

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
	ID            int64
	CampaignID    int64
	Name          string
	Email         string
	Wishlist      string
	SurprisesOpen bool
	Token         string
	CreatedAt     time.Time
}

// HasWish reports whether a wish has been entered. That is the only
// precondition for the draw, so it doubles as "ready".
func (p Participant) HasWish() bool { return strings.TrimSpace(p.Wishlist) != "" }

// Surprise is an idea left for a recipient, with the author's name attached
// for display.
type Surprise struct {
	ID          int64
	AuthorID    int64
	AuthorName  string
	RecipientID int64
	Body        string
	UpdatedAt   time.Time
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
		"&_pragma=foreign_keys(1)" +
		// Every transaction here reads and then writes. Taking the write
		// lock up front avoids the upgrade failing halfway through.
		"&_txlock=immediate"

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

const participantColumns = `id, campaign_id, name, email, wishlist, surprises_open, token, created_at`

func scanParticipant(row interface{ Scan(...any) error }) (Participant, error) {
	var p Participant
	var created string
	err := row.Scan(&p.ID, &p.CampaignID, &p.Name, &p.Email, &p.Wishlist,
		&p.SurprisesOpen, &p.Token, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return Participant{}, ErrNotFound
	}
	if err != nil {
		return Participant{}, err
	}
	p.CreatedAt, _ = time.Parse(time.RFC3339, created)
	return p, nil
}

// AddParticipant joins somebody to a campaign that is still open. Names are
// unique within a campaign, ignoring case and surrounding space.
func (s *Store) AddParticipant(ctx context.Context, campaignID int64, name, email, wishlist string) (Participant, error) {
	p := Participant{
		CampaignID:    campaignID,
		Name:          strings.TrimSpace(name),
		Email:         email,
		Wishlist:      wishlist,
		SurprisesOpen: true,
		Token:         token.New(),
		CreatedAt:     time.Now().UTC(),
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Participant{}, err
	}
	defer tx.Rollback()

	if err := requireOpen(ctx, tx, campaignID); err != nil {
		return Participant{}, err
	}
	// SQLite's lower() only folds ASCII, so compare in Go to catch "Jörg"
	// against "JÖRG" as well.
	existing, err := participants(ctx, tx, campaignID)
	if err != nil {
		return Participant{}, err
	}
	for _, e := range existing {
		if strings.EqualFold(strings.TrimSpace(e.Name), p.Name) {
			return Participant{}, ErrNameTaken
		}
	}

	res, err := tx.ExecContext(ctx,
		`INSERT INTO participant (campaign_id, name, email, wishlist, surprises_open, token, created_at)
		 VALUES (?, ?, ?, ?, 1, ?, ?)`,
		p.CampaignID, p.Name, p.Email, p.Wishlist, p.Token, p.CreatedAt.Format(time.RFC3339))
	if err != nil {
		return Participant{}, fmt.Errorf("insert participant: %w", err)
	}
	if p.ID, err = res.LastInsertId(); err != nil {
		return Participant{}, err
	}
	return p, tx.Commit()
}

// requireOpen fails with ErrNotFound or ErrAlreadyDrawn unless the campaign
// exists and has not been drawn. Run inside a transaction, the answer holds
// until it commits, since every transaction takes the write lock up front.
func requireOpen(ctx context.Context, q querier, campaignID int64) error {
	var state string
	err := q.QueryRowContext(ctx, `SELECT state FROM campaign WHERE id = ?`, campaignID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if state != StateOpen {
		return ErrAlreadyDrawn
	}
	return nil
}

// updateWhileOpen runs a single UPDATE or DELETE that names one row of a
// campaign, after checking the campaign is still open, and reports
// ErrNotFound when the statement touched nothing.
func (s *Store) updateWhileOpen(ctx context.Context, campaignID int64, query string, args ...any) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := requireOpen(ctx, tx, campaignID); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

// ParticipantByToken resolves somebody's private link.
func (s *Store) ParticipantByToken(ctx context.Context, t string) (Participant, error) {
	return scanParticipant(s.db.QueryRowContext(ctx,
		`SELECT `+participantColumns+` FROM participant WHERE token = ?`, t))
}

// querier is what *sql.DB and *sql.Tx have in common, so the list queries can
// run inside the draw transaction as well as on their own.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Participants lists a campaign in join order.
func (s *Store) Participants(ctx context.Context, campaignID int64) ([]Participant, error) {
	return participants(ctx, s.db, campaignID)
}

func participants(ctx context.Context, q querier, campaignID int64) ([]Participant, error) {
	rows, err := q.QueryContext(ctx,
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

// SetWishlist replaces somebody's wish list text. Wishes are final once the
// campaign is drawn, so the giver's view cannot change under them.
func (s *Store) SetWishlist(ctx context.Context, campaignID, participantID int64, text string) error {
	return s.updateWhileOpen(ctx, campaignID,
		`UPDATE participant SET wishlist = ? WHERE id = ? AND campaign_id = ?`,
		text, participantID, campaignID)
}

// SetSurprisesOpen decides whether others may leave surprise ideas for this
// participant. Ideas already left stay in place either way; the switch only
// governs new ones. It cannot change after the draw.
func (s *Store) SetSurprisesOpen(ctx context.Context, campaignID, participantID int64, open bool) error {
	return s.updateWhileOpen(ctx, campaignID,
		`UPDATE participant SET surprises_open = ? WHERE id = ? AND campaign_id = ?`,
		open, participantID, campaignID)
}

// DeleteParticipant removes somebody from a campaign that is still open. The
// campaign id is part of the condition so an organizer token cannot be used to
// reach into a different campaign.
func (s *Store) DeleteParticipant(ctx context.Context, campaignID, participantID int64) error {
	return s.updateWhileOpen(ctx, campaignID,
		`DELETE FROM participant WHERE id = ? AND campaign_id = ?`, participantID, campaignID)
}

// Exclusions lists the pairs who must not draw each other.
func (s *Store) Exclusions(ctx context.Context, campaignID int64) ([]Exclusion, error) {
	return exclusions(ctx, s.db, campaignID)
}

func exclusions(ctx context.Context, q querier, campaignID int64) ([]Exclusion, error) {
	rows, err := q.QueryContext(ctx,
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
		return ErrInvalid
	}
	if aID > bID {
		aID, bID = bID, aID
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := requireOpen(ctx, tx, campaignID); err != nil {
		return err
	}
	var n int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM participant WHERE campaign_id = ? AND id IN (?, ?)`,
		campaignID, aID, bID).Scan(&n); err != nil {
		return err
	}
	if n != 2 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO exclusion (campaign_id, a_id, b_id) VALUES (?, ?, ?)`,
		campaignID, aID, bID); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteExclusion drops a pair again, as long as the campaign is open.
func (s *Store) DeleteExclusion(ctx context.Context, campaignID, exclusionID int64) error {
	return s.updateWhileOpen(ctx, campaignID,
		`DELETE FROM exclusion WHERE id = ? AND campaign_id = ?`, exclusionID, campaignID)
}

// Draw performs the draw and stores it. Everything happens in one
// transaction that first claims the state transition, so a concurrent second
// draw or a late join cannot slip in between reading the group and writing
// the assignment, and a campaign never ends up drawn without a complete one.
// It refuses while anybody still lacks a wish.
func (s *Store) Draw(ctx context.Context, campaignID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx,
		`UPDATE campaign SET state = ? WHERE id = ? AND state = ?`,
		StateDrawn, campaignID, StateOpen)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrAlreadyDrawn
	}

	participants, err := participants(ctx, tx, campaignID)
	if err != nil {
		return err
	}
	ids := make([]int64, len(participants))
	for i, p := range participants {
		if !p.HasWish() {
			return ErrNotReady
		}
		ids[i] = p.ID
	}
	exclusions, err := exclusions(ctx, tx, campaignID)
	if err != nil {
		return err
	}
	pairs := make([]draw.Pair, len(exclusions))
	for i, e := range exclusions {
		pairs[i] = draw.Pair{A: e.AID, B: e.BID}
	}

	assignment, err := draw.Assign(ids, pairs)
	if err != nil {
		return err
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

// CampaignsByOrganizer lists the campaigns an organizer created, newest first.
func (s *Store) CampaignsByOrganizer(ctx context.Context, label string) ([]Campaign, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+campaignColumns+` FROM campaign
		 WHERE organizer_label = ? ORDER BY id DESC`, label)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Campaign
	for rows.Next() {
		c, err := scanCampaign(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SaveSurprise stores, replaces or (with an empty body) removes the idea an
// author leaves for a recipient. Both must belong to the campaign, the
// recipient must accept surprises, and the campaign must still be open.
// Removing an idea is always allowed before the draw, even after the
// recipient closed their surprises.
func (s *Store) SaveSurprise(ctx context.Context, campaignID, authorID, recipientID int64, body string) error {
	if authorID == recipientID {
		return ErrInvalid
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var state string
	var open bool
	var members int
	err = tx.QueryRowContext(ctx,
		`SELECT c.state, r.surprises_open,
		        (SELECT COUNT(*) FROM participant WHERE campaign_id = c.id AND id IN (?, ?))
		 FROM campaign c JOIN participant r ON r.campaign_id = c.id
		 WHERE c.id = ? AND r.id = ?`,
		authorID, recipientID, campaignID, recipientID).Scan(&state, &open, &members)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if members != 2 {
		return ErrNotFound
	}
	if state == StateDrawn {
		return ErrAlreadyDrawn
	}

	if strings.TrimSpace(body) == "" {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM surprise WHERE author_id = ? AND recipient_id = ?`,
			authorID, recipientID); err != nil {
			return err
		}
		return tx.Commit()
	}

	if !open {
		// Editing an idea that already exists stays possible; only adding a
		// new one is shut off by the recipient's switch.
		var exists int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM surprise WHERE author_id = ? AND recipient_id = ?`,
			authorID, recipientID).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return ErrSurprisesClosed
		}
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO surprise (campaign_id, author_id, recipient_id, body, updated_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT (author_id, recipient_id)
		 DO UPDATE SET body = excluded.body, updated_at = excluded.updated_at`,
		campaignID, authorID, recipientID, body, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	return tx.Commit()
}

// SurpriseBy returns the idea an author left for a recipient, or ErrNotFound.
func (s *Store) SurpriseBy(ctx context.Context, authorID, recipientID int64) (Surprise, error) {
	rows, err := s.db.QueryContext(ctx, surpriseQuery+
		` WHERE s.author_id = ? AND s.recipient_id = ?`, authorID, recipientID)
	if err != nil {
		return Surprise{}, err
	}
	out, err := scanSurprises(rows)
	if err != nil {
		return Surprise{}, err
	}
	if len(out) == 0 {
		return Surprise{}, ErrNotFound
	}
	return out[0], nil
}

// SurprisesForGiver returns the ideas left for whoever the giver drew. It is
// the only way to read other people's ideas, and it yields nothing before the
// draw, so a recipient can never see what was left for them.
func (s *Store) SurprisesForGiver(ctx context.Context, campaignID, giverID int64) ([]Surprise, error) {
	rows, err := s.db.QueryContext(ctx, surpriseQuery+`
		JOIN assignment g ON g.receiver_id = s.recipient_id
		JOIN campaign c ON c.id = g.campaign_id
		WHERE g.campaign_id = ? AND g.giver_id = ? AND c.state = ?
		ORDER BY s.id`, campaignID, giverID, StateDrawn)
	if err != nil {
		return nil, err
	}
	return scanSurprises(rows)
}

// SurpriseCounts reports how many ideas each participant of a campaign has
// received, keyed by recipient. Everybody may see these numbers.
func (s *Store) SurpriseCounts(ctx context.Context, campaignID int64) (map[int64]int, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT recipient_id, COUNT(*) FROM surprise
		 WHERE campaign_id = ? GROUP BY recipient_id`, campaignID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[int64]int{}
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// SurprisesByAuthor reports for which recipients an author has left an idea.
func (s *Store) SurprisesByAuthor(ctx context.Context, authorID int64) (map[int64]bool, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT recipient_id FROM surprise WHERE author_id = ?`, authorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

const surpriseQuery = `SELECT s.id, s.author_id, a.name, s.recipient_id, s.body, s.updated_at
	FROM surprise s JOIN participant a ON a.id = s.author_id`

func scanSurprises(rows *sql.Rows) ([]Surprise, error) {
	defer rows.Close()
	var out []Surprise
	for rows.Next() {
		var x Surprise
		var updated string
		if err := rows.Scan(&x.ID, &x.AuthorID, &x.AuthorName, &x.RecipientID, &x.Body, &updated); err != nil {
			return nil, err
		}
		x.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
		out = append(out, x)
	}
	return out, rows.Err()
}
