package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/mdhuser/spendwise-biz/internal/ledger/domain"
	"github.com/mdhuser/spendwise-biz/internal/platform/db"
)

type goalCursor struct {
	Time int64  `json:"t"`
	ID   string `json:"id"`
}

type GoalPage struct {
	Items      []domain.SavingGoal
	NextCursor string
}

func encodeGoalCursor(t time.Time, id uuid.UUID) string {
	raw, _ := json.Marshal(goalCursor{Time: t.UnixMicro(), ID: id.String()})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func (s *Store) CreateGoal(ctx context.Context, q queryer, goal *domain.SavingGoal) error {
	_, err := q.ExecContext(ctx, `
		INSERT INTO saving_goals
		(id, owner_id, name, status, start_period, end_period, version, archived_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		db.UUIDBytes(goal.ID), db.UUIDBytes(goal.OwnerID), goal.Name, goal.Status,
		goal.StartPeriod.Start(time.UTC), nullablePeriod(goal.EndPeriod), goal.Version,
		goal.ArchivedAt, goal.CreatedAt, goal.UpdatedAt)
	return err
}

func (s *Store) GetGoal(ctx context.Context, ownerID, id uuid.UUID) (*domain.SavingGoal, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, owner_id, name, status, start_period, end_period, version, archived_at,
		       created_at, updated_at
		FROM saving_goals WHERE id = ? AND owner_id = ?`,
		db.UUIDBytes(id), db.UUIDBytes(ownerID))
	return scanGoal(row)
}

func (s *Store) GetGoalForUpdate(ctx context.Context, tx *sql.Tx, ownerID, id uuid.UUID) (*domain.SavingGoal, error) {
	row := tx.QueryRowContext(ctx, `
		SELECT id, owner_id, name, status, start_period, end_period, version, archived_at,
		       created_at, updated_at
		FROM saving_goals WHERE id = ? AND owner_id = ? FOR UPDATE`,
		db.UUIDBytes(id), db.UUIDBytes(ownerID))
	return scanGoal(row)
}

func scanGoal(row interface{ Scan(...any) error }) (*domain.SavingGoal, error) {
	var goal domain.SavingGoal
	var id, owner []byte
	var start time.Time
	var end, archived sql.NullTime
	if err := row.Scan(&id, &owner, &goal.Name, &goal.Status, &start,
		&end, &goal.Version, &archived, &goal.CreatedAt, &goal.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, err
	}
	var err error
	if goal.ID, err = db.UUIDFromBytes(id); err != nil {
		return nil, err
	}
	if goal.OwnerID, err = db.UUIDFromBytes(owner); err != nil {
		return nil, err
	}
	goal.StartPeriod = domain.PeriodFromDate(start)
	if end.Valid {
		period := domain.PeriodFromDate(end.Time)
		goal.EndPeriod = &period
	}
	if archived.Valid {
		goal.ArchivedAt = &archived.Time
	}
	return &goal, nil
}

func (s *Store) ListGoals(ctx context.Context, ownerID uuid.UUID, status, cursor string, limit int) (GoalPage, error) {
	query := `SELECT id, owner_id, name, status, start_period, end_period, version, archived_at,
	                 created_at, updated_at
	          FROM saving_goals WHERE owner_id = ?`
	args := []any{db.UUIDBytes(ownerID)}
	if status != "" {
		query += ` AND status = ?`
		args = append(args, status)
	}
	if cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil {
			return GoalPage{}, ErrConflict
		}
		var c goalCursor
		if err := json.Unmarshal(raw, &c); err != nil {
			return GoalPage{}, ErrConflict
		}
		id, err := uuid.Parse(c.ID)
		if err != nil {
			return GoalPage{}, ErrConflict
		}
		query += ` AND (created_at < ? OR (created_at = ? AND id < ?))`
		args = append(args, time.UnixMicro(c.Time).UTC(), time.UnixMicro(c.Time).UTC(), db.UUIDBytes(id))
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return GoalPage{}, err
	}
	defer rows.Close()
	var items []domain.SavingGoal
	for rows.Next() {
		goal, err := scanGoal(rows)
		if err != nil {
			return GoalPage{}, err
		}
		items = append(items, *goal)
	}
	if err := rows.Err(); err != nil {
		return GoalPage{}, err
	}
	page := GoalPage{Items: items}
	if len(items) > limit {
		page.Items = items[:limit]
		last := page.Items[len(page.Items)-1]
		page.NextCursor = encodeGoalCursor(last.CreatedAt, last.ID)
	}
	return page, nil
}

func (s *Store) UpdateGoal(ctx context.Context, q queryer, goal *domain.SavingGoal, expectedVersion int) error {
	result, err := q.ExecContext(ctx, `
		UPDATE saving_goals
		SET name = ?, status = ?, end_period = ?, version = version + 1,
		    archived_at = ?, updated_at = ?
		WHERE id = ? AND owner_id = ? AND version = ?`,
		goal.Name, goal.Status, nullablePeriod(goal.EndPeriod), goal.ArchivedAt,
		goal.UpdatedAt, db.UUIDBytes(goal.ID), db.UUIDBytes(goal.OwnerID), expectedVersion)
	if err != nil {
		return err
	}
	return expectOne(result)
}

func nullablePeriod(period *domain.Period) any {
	if period == nil {
		return nil
	}
	return period.Start(time.UTC)
}

func (s *Store) InsertTerm(ctx context.Context, q queryer, term *domain.GoalTerm, categoryIDs []uuid.UUID) error {
	_, err := q.ExecContext(ctx, `
		INSERT INTO saving_goal_terms
		(id, goal_id, owner_id, version, effective_from_period, scope_kind, baseline_kind,
		 baseline_fen, initial_baseline_fen, target_saving_fen, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		db.UUIDBytes(term.ID), db.UUIDBytes(term.GoalID), db.UUIDBytes(term.OwnerID),
		term.Version, term.EffectiveFromPeriod.Start(time.UTC), term.ScopeKind,
		term.BaselineKind, term.BaselineFen, term.InitialBaselineFen,
		term.TargetSavingFen, term.CreatedAt)
	if err != nil {
		return err
	}
	for _, categoryID := range categoryIDs {
		if _, err := q.ExecContext(ctx, `
			INSERT INTO saving_goal_term_categories (term_id, owner_id, category_id)
			VALUES (?, ?, ?)`,
			db.UUIDBytes(term.ID), db.UUIDBytes(term.OwnerID), db.UUIDBytes(categoryID)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ListTerms(ctx context.Context, ownerID, goalID uuid.UUID) ([]domain.GoalTerm, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, goal_id, owner_id, version, effective_from_period, scope_kind,
		       baseline_kind, baseline_fen, initial_baseline_fen, target_saving_fen, created_at
		FROM saving_goal_terms
		WHERE goal_id = ? AND owner_id = ?
		ORDER BY version ASC`, db.UUIDBytes(goalID), db.UUIDBytes(ownerID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var terms []domain.GoalTerm
	for rows.Next() {
		term, err := scanTerm(rows)
		if err != nil {
			return nil, err
		}
		terms = append(terms, *term)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range terms {
		categoryIDs, err := s.termCategories(ctx, ownerID, terms[i].ID)
		if err != nil {
			return nil, err
		}
		terms[i].CategoryIDs = categoryIDs
	}
	return terms, nil
}

func (s *Store) MaxTermVersion(ctx context.Context, q queryer, goalID uuid.UUID) (int, error) {
	var version int
	err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM saving_goal_terms WHERE goal_id = ?`, db.UUIDBytes(goalID)).Scan(&version)
	return version, err
}

func scanTerm(row interface{ Scan(...any) error }) (*domain.GoalTerm, error) {
	var term domain.GoalTerm
	var id, goalID, owner []byte
	var effective time.Time
	var baseline, initial sql.NullInt64
	if err := row.Scan(&id, &goalID, &owner, &term.Version, &effective,
		&term.ScopeKind, &term.BaselineKind, &baseline, &initial,
		&term.TargetSavingFen, &term.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, err
	}
	var err error
	if term.ID, err = db.UUIDFromBytes(id); err != nil {
		return nil, err
	}
	if term.GoalID, err = db.UUIDFromBytes(goalID); err != nil {
		return nil, err
	}
	if term.OwnerID, err = db.UUIDFromBytes(owner); err != nil {
		return nil, err
	}
	term.EffectiveFromPeriod = domain.PeriodFromDate(effective)
	if baseline.Valid {
		value := baseline.Int64
		term.BaselineFen = &value
	}
	if initial.Valid {
		value := initial.Int64
		term.InitialBaselineFen = &value
	}
	return &term, nil
}

func (s *Store) termCategories(ctx context.Context, ownerID, termID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT category_id FROM saving_goal_term_categories
		WHERE term_id = ? AND owner_id = ?`, db.UUIDBytes(termID), db.UUIDBytes(ownerID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		id, err := db.UUIDFromBytes(raw)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) LatestTermEffective(ctx context.Context, q queryer, goalID uuid.UUID) (*time.Time, error) {
	var value time.Time
	err := q.QueryRowContext(ctx, `
		SELECT effective_from_period FROM saving_goal_terms
		WHERE goal_id = ? ORDER BY effective_from_period DESC LIMIT 1`,
		db.UUIDBytes(goalID)).Scan(&value)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &value, nil
}

func (s *Store) GetGoalTx(ctx context.Context, q queryer, ownerID, id uuid.UUID) (*domain.SavingGoal, error) {
	row := q.QueryRowContext(ctx, `
		SELECT id, owner_id, name, status, start_period, end_period, version, archived_at,
		       created_at, updated_at
		FROM saving_goals WHERE id = ? AND owner_id = ?`, db.UUIDBytes(id), db.UUIDBytes(ownerID))
	return scanGoal(row)
}

func (s *Store) ListGoalsTx(ctx context.Context, q queryer, ownerID uuid.UUID, status string, limit int) ([]domain.SavingGoal, error) {
	query := `SELECT id, owner_id, name, status, start_period, end_period, version, archived_at,
	                 created_at, updated_at
	          FROM saving_goals WHERE owner_id = ?`
	args := []any{db.UUIDBytes(ownerID)}
	if status != "" {
		query += ` AND status = ?`
		args = append(args, status)
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.SavingGoal
	for rows.Next() {
		goal, err := scanGoal(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *goal)
	}
	return out, rows.Err()
}

func (s *Store) ListTermsTx(ctx context.Context, q queryer, ownerID, goalID uuid.UUID) ([]domain.GoalTerm, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT id, goal_id, owner_id, version, effective_from_period, scope_kind,
		       baseline_kind, baseline_fen, initial_baseline_fen, target_saving_fen, created_at
		FROM saving_goal_terms
		WHERE goal_id = ? AND owner_id = ?
		ORDER BY version ASC`, db.UUIDBytes(goalID), db.UUIDBytes(ownerID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var terms []domain.GoalTerm
	for rows.Next() {
		term, err := scanTerm(rows)
		if err != nil {
			return nil, err
		}
		terms = append(terms, *term)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range terms {
		categoryIDs, err := s.termCategoriesTx(ctx, q, ownerID, terms[i].ID)
		if err != nil {
			return nil, err
		}
		terms[i].CategoryIDs = categoryIDs
	}
	return terms, nil
}

func (s *Store) termCategoriesTx(ctx context.Context, q queryer, ownerID, termID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT category_id FROM saving_goal_term_categories
		WHERE term_id = ? AND owner_id = ?`, db.UUIDBytes(termID), db.UUIDBytes(ownerID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		id, err := db.UUIDFromBytes(raw)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
