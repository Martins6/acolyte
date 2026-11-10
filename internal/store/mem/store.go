package mem

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"
)

// Store gives access to the memory tables.
type Store struct {
	db *sql.DB
}

// New returns a memory store on an open database.
func New(db *sql.DB) *Store { return &Store{db: db} }

// Dream is one recorded memory-learn run.
type Dream struct {
	ID              string    `json:"id"`
	RanAt           time.Time `json:"ran_at"`
	SessionsScanned int       `json:"sessions_scanned"`
	SkillsCreated   int       `json:"skills_created"`
	DryRun          bool      `json:"dry_run"`
	Note            string    `json:"note"`
}

// RecordDream appends one dream audit row.
func (s *Store) RecordDream(dream Dream) error {
	if dream.ID == "" {
		id, err := newID()
		if err != nil {
			return err
		}
		dream.ID = id
	}
	if dream.RanAt.IsZero() {
		dream.RanAt = time.Now().UTC()
	}
	_, err := s.db.Exec(`INSERT INTO mem_dreams
		(id, ran_at, sessions_scanned, skills_created, dry_run, note)
		VALUES (?, ?, ?, ?, ?, ?)`,
		dream.ID, dream.RanAt, dream.SessionsScanned, dream.SkillsCreated, boolInt(dream.DryRun), dream.Note,
	)
	if err != nil {
		return fmt.Errorf("mem: record dream: %w", err)
	}
	return nil
}

// ListDreams returns recorded memory-learn runs in chronological order.
func (s *Store) ListDreams(limit int) ([]Dream, error) {
	query := `SELECT id, ran_at, sessions_scanned, skills_created, dry_run, note
		FROM mem_dreams ORDER BY ran_at, id`
	args := []any{}
	if limit > 0 {
		query = `SELECT id, ran_at, sessions_scanned, skills_created, dry_run, note FROM (
			SELECT id, ran_at, sessions_scanned, skills_created, dry_run, note
			FROM mem_dreams ORDER BY ran_at DESC, id DESC LIMIT ?
		) ORDER BY ran_at, id`
		args = append(args, limit)
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("mem: list dreams: %w", err)
	}
	defer rows.Close()

	var dreams []Dream
	for rows.Next() {
		var dream Dream
		var dryRun int
		if err := rows.Scan(&dream.ID, &dream.RanAt, &dream.SessionsScanned,
			&dream.SkillsCreated, &dryRun, &dream.Note); err != nil {
			return nil, fmt.Errorf("mem: scan dream: %w", err)
		}
		dream.DryRun = dryRun != 0
		dreams = append(dreams, dream)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("mem: list dreams: %w", err)
	}
	return dreams, nil
}

func newID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("mem: generate id: %w", err)
	}
	return "mem_dream_" + hex.EncodeToString(raw[:]), nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
