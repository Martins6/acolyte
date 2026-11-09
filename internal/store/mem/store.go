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
