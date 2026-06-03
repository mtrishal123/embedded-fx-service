package settlement

import (
	"database/sql"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/shopspring/decimal"

	_ "github.com/lib/pq"

	"github.com/mtrishal123/embedded-fx-service/internal/fx"
)

// -------------------------------------------------------------------
// MemoryRepository — in-memory, thread-safe
// -------------------------------------------------------------------

// MemoryRepository stores settlements in a map guarded by a mutex.
// Data does not survive a restart — it's for local dev and tests only.
type MemoryRepository struct {
	mu   sync.RWMutex
	data map[string]*Settlement
}

// NewMemoryRepository creates an empty in-memory repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		data: make(map[string]*Settlement),
	}
}

// Create stores a new settlement. It rejects duplicate IDs.
func (r *MemoryRepository) Create(s *Settlement) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.data[s.ID]; exists {
		return fmt.Errorf("settlement %s already exists", s.ID)
	}
	// Store a copy so callers can't mutate our stored state by accident.
	clone := *s
	r.data[s.ID] = &clone
	return nil
}

// GetByID returns a copy of the settlement, or an error if not found.
func (r *MemoryRepository) GetByID(id string) (*Settlement, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	s, ok := r.data[id]
	if !ok {
		return nil, fmt.Errorf("settlement not found: %s", id)
	}
	clone := *s
	return &clone, nil
}

// UpdateStatus transitions a settlement to a new status. When moving to
// SETTLED it stamps SettledAt; when moving to FAILED it records the reason.
func (r *MemoryRepository) UpdateStatus(id string, status Status, failureReason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	s, ok := r.data[id]
	if !ok {
		return fmt.Errorf("settlement not found: %s", id)
	}

	now := time.Now().UTC()
	s.Status = status
	s.UpdatedAt = now

	switch status {
	case StatusSettled:
		s.SettledAt = &now
		s.FailureReason = ""
	case StatusFailed:
		s.FailureReason = failureReason
	}
	return nil
}

// ListByPartner returns up to `limit` of a partner's settlements,
// most recent first.
func (r *MemoryRepository) ListByPartner(partnerID string, limit int) ([]*Settlement, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var out []*Settlement
	for _, s := range r.data {
		if s.PartnerID == partnerID {
			clone := *s
			out = append(out, &clone)
		}
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})

	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// -------------------------------------------------------------------
// PostgresRepository — production implementation
// -------------------------------------------------------------------

// PostgresRepository persists settlements in PostgreSQL using database/sql.
type PostgresRepository struct {
	db *sql.DB
}

// NewPostgresRepository opens a connection pool to Postgres and verifies it
// with a Ping. The DSN looks like:
//
//	"host=localhost port=5432 user=fx password=fx dbname=fx sslmode=disable"
func NewPostgresRepository(dsn string) (*PostgresRepository, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}

	// Sensible pool limits for a small service.
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	return &PostgresRepository{db: db}, nil
}

// Close releases the underlying connection pool.
func (r *PostgresRepository) Close() error {
	return r.db.Close()
}

// Create inserts a new settlement row.
func (r *PostgresRepository) Create(s *Settlement) error {
	const q = `
		INSERT INTO settlements (
			id, partner_id, user_id,
			source_amount, source_currency,
			target_amount, target_currency,
			applied_rate, mid_market_rate, spread_cost,
			status, failure_reason,
			created_at, updated_at, settled_at
		) VALUES (
			$1, $2, $3,
			$4, $5,
			$6, $7,
			$8, $9, $10,
			$11, $12,
			$13, $14, $15
		)`

	_, err := r.db.Exec(q,
		s.ID, s.PartnerID, s.UserID,
		s.SourceAmount.String(), string(s.SourceCurrency),
		s.TargetAmount.String(), string(s.TargetCurrency),
		s.AppliedRate.String(), s.MidMarketRate.String(), s.SpreadCost.String(),
		string(s.Status), s.FailureReason,
		s.CreatedAt, s.UpdatedAt, s.SettledAt,
	)
	if err != nil {
		return fmt.Errorf("insert settlement: %w", err)
	}
	return nil
}

// GetByID fetches a single settlement by its UUID.
func (r *PostgresRepository) GetByID(id string) (*Settlement, error) {
	const q = `
		SELECT id, partner_id, user_id,
		       source_amount, source_currency,
		       target_amount, target_currency,
		       applied_rate, mid_market_rate, spread_cost,
		       status, failure_reason,
		       created_at, updated_at, settled_at
		FROM settlements
		WHERE id = $1`

	return scanSettlement(r.db.QueryRow(q, id))
}

// UpdateStatus updates status and, depending on the target state, the
// settled_at timestamp or failure reason.
func (r *PostgresRepository) UpdateStatus(id string, status Status, failureReason string) error {
	now := time.Now().UTC()

	var (
		settledAt *time.Time
		reason    string
	)
	switch status {
	case StatusSettled:
		settledAt = &now
	case StatusFailed:
		reason = failureReason
	}

	const q = `
		UPDATE settlements
		SET status = $2,
		    failure_reason = $3,
		    settled_at = COALESCE($4, settled_at),
		    updated_at = $5
		WHERE id = $1`

	res, err := r.db.Exec(q, id, string(status), reason, settledAt, now)
	if err != nil {
		return fmt.Errorf("update settlement status: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("settlement not found: %s", id)
	}
	return nil
}

// ListByPartner returns up to `limit` settlements for a partner, newest first.
func (r *PostgresRepository) ListByPartner(partnerID string, limit int) ([]*Settlement, error) {
	const q = `
		SELECT id, partner_id, user_id,
		       source_amount, source_currency,
		       target_amount, target_currency,
		       applied_rate, mid_market_rate, spread_cost,
		       status, failure_reason,
		       created_at, updated_at, settled_at
		FROM settlements
		WHERE partner_id = $1
		ORDER BY created_at DESC
		LIMIT $2`

	rows, err := r.db.Query(q, partnerID, limit)
	if err != nil {
		return nil, fmt.Errorf("list settlements: %w", err)
	}
	defer rows.Close()

	var out []*Settlement
	for rows.Next() {
		s, err := scanSettlement(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// scanner is satisfied by both *sql.Row and *sql.Rows, so we can share one
// scanning routine between GetByID and ListByPartner.
type scanner interface {
	Scan(dest ...any) error
}

func scanSettlement(row scanner) (*Settlement, error) {
	var (
		s             Settlement
		sourceAmount  string
		targetAmount  string
		appliedRate   string
		midMarketRate string
		spreadCost    string
		sourceCurr    string
		targetCurr    string
		status        string
		failureReason sql.NullString
		settledAt     sql.NullTime
	)

	err := row.Scan(
		&s.ID, &s.PartnerID, &s.UserID,
		&sourceAmount, &sourceCurr,
		&targetAmount, &targetCurr,
		&appliedRate, &midMarketRate, &spreadCost,
		&status, &failureReason,
		&s.CreatedAt, &s.UpdatedAt, &settledAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("settlement not found")
		}
		return nil, fmt.Errorf("scan settlement: %w", err)
	}

	// Parse decimals back from their string representation.
	if s.SourceAmount, err = decimal.NewFromString(sourceAmount); err != nil {
		return nil, fmt.Errorf("parse source_amount: %w", err)
	}
	if s.TargetAmount, err = decimal.NewFromString(targetAmount); err != nil {
		return nil, fmt.Errorf("parse target_amount: %w", err)
	}
	if s.AppliedRate, err = decimal.NewFromString(appliedRate); err != nil {
		return nil, fmt.Errorf("parse applied_rate: %w", err)
	}
	if s.MidMarketRate, err = decimal.NewFromString(midMarketRate); err != nil {
		return nil, fmt.Errorf("parse mid_market_rate: %w", err)
	}
	if s.SpreadCost, err = decimal.NewFromString(spreadCost); err != nil {
		return nil, fmt.Errorf("parse spread_cost: %w", err)
	}

	s.SourceCurrency = fx.Currency(sourceCurr)
	s.TargetCurrency = fx.Currency(targetCurr)
	s.Status = Status(status)
	if failureReason.Valid {
		s.FailureReason = failureReason.String
	}
	if settledAt.Valid {
		t := settledAt.Time
		s.SettledAt = &t
	}

	return &s, nil
}
