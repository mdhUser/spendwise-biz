package app

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mdhuser/spendwise-biz/internal/ledger/domain"
	ledgerstore "github.com/mdhuser/spendwise-biz/internal/ledger/store/mysql"
	"github.com/mdhuser/spendwise-biz/internal/platform/apperr"
	"github.com/mdhuser/spendwise-biz/internal/platform/db"
	"github.com/mdhuser/spendwise-biz/internal/platform/id"
)

type GoalScopeInput struct {
	Kind        string      `json:"kind"`
	CategoryIDs []uuid.UUID `json:"categoryIds"`
}

type GoalBaselineInput struct {
	Kind               string `json:"kind"`
	BaselineFen        *int64 `json:"baselineFen"`
	InitialBaselineFen *int64 `json:"initialBaselineFen"`
}

type CreateGoalInput struct {
	Name            string            `json:"name"`
	StartPeriod     string            `json:"startPeriod"`
	EndPeriod       *string           `json:"endPeriod"`
	Scope           GoalScopeInput    `json:"scope"`
	Baseline        GoalBaselineInput `json:"baseline"`
	TargetSavingFen int64             `json:"targetSavingFen"`
}

type UpdateGoalInput struct {
	Name      *string `json:"name"`
	EndPeriod *string `json:"endPeriod"`
	Status    *string `json:"status"`
	Version   int     `json:"version"`
}

type AddTermInput struct {
	EffectiveFromPeriod string            `json:"effectiveFromPeriod"`
	Scope               GoalScopeInput    `json:"scope"`
	Baseline            GoalBaselineInput `json:"baseline"`
	TargetSavingFen     int64             `json:"targetSavingFen"`
}

func (s *Service) ListGoals(ctx context.Context, ownerID string, status, cursor string, limit int) (any, error) {
	if status == "" {
		status = domain.GoalActive
	}
	if limit < 1 || limit > 100 {
		limit = 50
	}
	page, err := s.store.ListGoals(ctx, ownerID, status, cursor, limit)
	if err != nil {
		return nil, err
	}
	items := make([]any, 0, len(page.Items))
	for i := range page.Items {
		terms, err := s.store.ListTerms(ctx, ownerID, page.Items[i].ID)
		if err != nil {
			return nil, err
		}
		items = append(items, s.goalView(&page.Items[i], terms))
	}
	return map[string]any{"items": items, "nextCursor": nullableString(page.NextCursor)}, nil
}

func (s *Service) GetGoal(ctx context.Context, ownerID string, goalID uuid.UUID) (any, error) {
	goal, err := s.store.GetGoal(ctx, ownerID, goalID)
	if err != nil {
		return nil, apperr.NotFound("目标不存在")
	}
	terms, err := s.store.ListTerms(ctx, ownerID, goalID)
	if err != nil {
		return nil, err
	}
	return s.goalView(goal, terms), nil
}

func (s *Service) CreateGoal(ctx context.Context, ownerID, actorID string, requestID, key string, input CreateGoalInput) (any, bool, error) {
	now := time.Now().UTC()
	current := domain.PeriodFromTime(now, s.loc)
	if input.StartPeriod == "" {
		input.StartPeriod = current.String()
	}
	start, err := domain.ParsePeriod(input.StartPeriod)
	if err != nil {
		return nil, false, apperr.Validation("startPeriod 必须是 YYYY-MM")
	}
	if start.Before(current.AddMonths(-24)) || start.After(current.AddMonths(12)) {
		return nil, false, apperr.Validation("startPeriod 超出允许范围")
	}
	var end *domain.Period
	if input.EndPeriod != nil && strings.TrimSpace(*input.EndPeriod) != "" {
		parsed, err := domain.ParsePeriod(*input.EndPeriod)
		if err != nil {
			return nil, false, apperr.Validation("endPeriod 必须是 YYYY-MM")
		}
		if parsed.Before(start) {
			return nil, false, apperr.Validation("endPeriod 不能早于 startPeriod")
		}
		if len(domain.PeriodsBetween(start, parsed)) > 60 {
			return nil, false, apperr.Validation("目标结束期间不能超过 60 个月")
		}
		end = &parsed
	}
	name := strings.TrimSpace(input.Name)
	if input.StartPeriod == "" {
		input.StartPeriod = current.String()
	}
	if len([]rune(name)) < 1 || len([]rune(name)) > 60 {
		return nil, false, apperr.Validation("目标名称长度必须在 1-60 个字符之间")
	}
	scopeKind, categoryIDs, err := s.validateScope(ctx, ownerID, input.Scope)
	if err != nil {
		return nil, false, err
	}
	if err := s.validateBaseline(input.Baseline, input.TargetSavingFen); err != nil {
		return nil, false, err
	}
	goal := &domain.SavingGoal{
		ID: id.New(), OwnerID: ownerID, Name: name, Status: domain.GoalActive,
		StartPeriod: start, EndPeriod: end, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	term := &domain.GoalTerm{
		ID: id.New(), GoalID: goal.ID, OwnerID: ownerID, Version: 1,
		EffectiveFromPeriod: start, ScopeKind: scopeKind, CategoryIDs: categoryIDs,
		BaselineKind: input.Baseline.Kind, BaselineFen: input.Baseline.BaselineFen,
		InitialBaselineFen: input.Baseline.InitialBaselineFen,
		TargetSavingFen:    input.TargetSavingFen, CreatedAt: now,
	}
	body, replay, err := s.idempotent(ctx, ownerID, key, "POST /v1/ledger/saving-goals", input, func(tx *sql.Tx) (any, *uuid.UUID, error) {
		if err := s.store.CreateGoal(ctx, tx, goal); err != nil {
			return nil, nil, err
		}
		if err := s.store.InsertTerm(ctx, tx, term, categoryIDs); err != nil {
			if db.IsDuplicate(err) {
				return nil, nil, apperr.Conflict("CONFLICT", "该生效期间已有条款")
			}
			return nil, nil, err
		}
		if err := s.store.InsertAudit(ctx, tx, ledgerstore.AuditEvent{
			OwnerID: ownerID, Entity: "saving_goal", EntityID: goal.ID, Action: "create",
			ActorID: actorID, RequestID: requestID, After: s.goalView(goal, []domain.GoalTerm{*term}),
		}); err != nil {
			return nil, nil, err
		}
		return s.goalView(goal, []domain.GoalTerm{*term}), &goal.ID, nil
	})
	return body, replay, err
}

func (s *Service) UpdateGoal(ctx context.Context, ownerID, actorID string, goalID uuid.UUID, requestID string, input UpdateGoalInput) (any, error) {
	goal, err := s.store.GetGoal(ctx, ownerID, goalID)
	if err != nil {
		return nil, apperr.NotFound("目标不存在")
	}
	before := s.goalView(goal, nil)
	terms, err := s.store.ListTerms(ctx, ownerID, goalID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	current := domain.PeriodFromTime(now, s.loc)
	if input.Name != nil {
		name := strings.TrimSpace(*input.Name)
		if len([]rune(name)) < 1 || len([]rune(name)) > 60 {
			return nil, apperr.Validation("目标名称长度必须在 1-60 个字符之间")
		}
		goal.Name = name
	}
	if input.EndPeriod != nil {
		if strings.TrimSpace(*input.EndPeriod) == "" {
			goal.EndPeriod = nil
		} else {
			end, err := domain.ParsePeriod(*input.EndPeriod)
			if err != nil {
				return nil, apperr.Validation("endPeriod 必须是 YYYY-MM")
			}
			if end.Before(current) {
				return nil, apperr.Validation("endPeriod 不能早于当前月")
			}
			if len(terms) > 0 && end.Before(terms[len(terms)-1].EffectiveFromPeriod) {
				return nil, apperr.Validation("endPeriod 不能早于最后一条条款")
			}
			if len(domain.PeriodsBetween(goal.StartPeriod, end)) > 60 {
				return nil, apperr.Validation("目标结束期间不能超过 60 个月")
			}
			goal.EndPeriod = &end
		}
	}
	if input.Status != nil {
		switch *input.Status {
		case domain.GoalActive:
			goal.Status = domain.GoalActive
			goal.ArchivedAt = nil
		case domain.GoalArchived:
			goal.Status = domain.GoalArchived
			goal.ArchivedAt = &now
		default:
			return nil, apperr.Validation("目标状态不合法")
		}
	}
	goal.UpdatedAt = now
	err = db.WithTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		if err := s.store.UpdateGoal(ctx, tx, goal, input.Version); err != nil {
			if errors.Is(err, ledgerstore.ErrConflict) {
				return apperr.Conflict("VERSION_CONFLICT", "目标已被修改，请刷新后重试")
			}
			return err
		}
		if err := s.store.InsertAudit(ctx, tx, ledgerstore.AuditEvent{
			OwnerID: ownerID, Entity: "saving_goal", EntityID: goal.ID, Action: "update",
			ActorID: actorID, RequestID: requestID, Before: before,
			After: s.goalView(goal, terms),
		}); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	goal.Version++
	return s.goalView(goal, terms), nil
}

func (s *Service) AddTerm(ctx context.Context, ownerID, actorID string, goalID uuid.UUID, requestID, key string, input AddTermInput) (any, bool, error) {
	effective, err := domain.ParsePeriod(input.EffectiveFromPeriod)
	if err != nil {
		return nil, false, apperr.Validation("effectiveFromPeriod 必须是 YYYY-MM")
	}
	now := time.Now().UTC()
	current := domain.PeriodFromTime(now, s.loc)
	if !effective.After(current) {
		return nil, false, apperr.Validation("新条款必须从下一期或更晚生效")
	}
	scopeKind, categoryIDs, err := s.validateScope(ctx, ownerID, input.Scope)
	if err != nil {
		return nil, false, err
	}
	if err := s.validateBaseline(input.Baseline, input.TargetSavingFen); err != nil {
		return nil, false, err
	}
	term := &domain.GoalTerm{
		ID: id.New(), GoalID: goalID, OwnerID: ownerID, EffectiveFromPeriod: effective,
		ScopeKind: scopeKind, CategoryIDs: categoryIDs, BaselineKind: input.Baseline.Kind,
		BaselineFen: input.Baseline.BaselineFen, InitialBaselineFen: input.Baseline.InitialBaselineFen,
		TargetSavingFen: input.TargetSavingFen, CreatedAt: now,
	}
	body, replay, err := s.idempotent(ctx, ownerID, key, "POST /v1/ledger/saving-goals/"+goalID.String()+"/terms", input, func(tx *sql.Tx) (any, *uuid.UUID, error) {
		goal, err := s.store.GetGoalForUpdate(ctx, tx, ownerID, goalID)
		if err != nil {
			return nil, nil, apperr.NotFound("目标不存在")
		}
		if goal.Status != domain.GoalActive {
			return nil, nil, apperr.Validation("归档目标不能新增条款")
		}
		if goal.EndPeriod != nil && effective.After(*goal.EndPeriod) {
			return nil, nil, apperr.Validation("条款生效期间超出目标结束期间")
		}
		latest, err := s.store.LatestTermEffective(ctx, tx, goalID)
		if err != nil {
			return nil, nil, err
		}
		if latest != nil && !effective.After(domain.PeriodFromDate(*latest)) {
			return nil, nil, apperr.Validation("条款生效期间必须晚于上一条款")
		}
		maxVersion, err := s.store.MaxTermVersion(ctx, tx, goalID)
		if err != nil {
			return nil, nil, err
		}
		term.Version = maxVersion + 1
		if err := s.store.InsertTerm(ctx, tx, term, categoryIDs); err != nil {
			if db.IsDuplicate(err) {
				return nil, nil, apperr.Conflict("CONFLICT", "该生效期间已有条款")
			}
			return nil, nil, err
		}
		if err := s.store.InsertAudit(ctx, tx, ledgerstore.AuditEvent{
			OwnerID: ownerID, Entity: "saving_goal_term", EntityID: term.ID, Action: "create",
			ActorID: actorID, RequestID: requestID, After: termView(term),
		}); err != nil {
			return nil, nil, err
		}
		return termView(term), &term.ID, nil
	})
	return body, replay, err
}

func (s *Service) Achievements(ctx context.Context, ownerID string, goalID uuid.UUID, from, to domain.Period, includeProjection bool) (any, error) {
	now := time.Now().UTC()
	current := domain.PeriodFromTime(now, s.loc)
	if to.After(current) {
		to = current
	}
	if from.After(to) {
		return nil, apperr.Validation("from 不能晚于 to")
	}
	if len(domain.PeriodsBetween(from, to)) > 36 {
		return nil, apperr.Validation("查询范围最多 36 个月")
	}

	var goal *domain.SavingGoal
	var terms []domain.GoalTerm
	var rows []domain.AchievementRow
	var summary domain.AchievementSummary
	err := db.WithTx(ctx, s.db, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}, func(tx *sql.Tx) error {
		var err error
		goal, err = s.store.GetGoalTx(ctx, tx, ownerID, goalID)
		if err != nil {
			return err
		}
		terms, err = s.store.ListTermsTx(ctx, tx, ownerID, goalID)
		if err != nil {
			return err
		}
		monthly, err := s.store.MonthlySpendTx(ctx, tx, ownerID,
			from.Start(s.loc).AddDate(0, -1, 0).UTC(), to.End(s.loc).UTC())
		if err != nil {
			return err
		}
		rows, summary = domain.BuildAchievements(domain.AchievementInput{
			Goal: *goal, Terms: terms, Monthly: monthly, From: from, To: to, Now: now, Loc: s.loc,
		})
		if !includeProjection {
			for i := range rows {
				rows[i].ProjectedSpendFen = nil
				rows[i].ProjectedSavedFen = nil
				rows[i].ProjectionKind = nil
				rows[i].OnTrack = nil
			}
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil, apperr.NotFound("目标不存在")
		}
		return nil, err
	}
	termViews := make([]any, 0, len(terms))
	for i := range terms {
		termViews = append(termViews, termView(&terms[i]))
	}
	return map[string]any{
		"goal": map[string]any{
			"id": goal.ID, "name": goal.Name, "status": goal.Status,
			"startPeriod": goal.StartPeriod.String(), "endPeriod": nullablePeriodString(goal.EndPeriod),
			"currency": "CNY",
		},
		"range": map[string]any{
			"from": from.String(), "to": to.String(), "timezone": s.cfg.Timezone,
		},
		"terms": termViews, "rows": rows, "summary": summary,
	}, nil
}

func (s *Service) Overview(ctx context.Context, ownerID string, period domain.Period) (any, error) {
	items := make([]any, 0)
	now := time.Now().UTC()
	current := domain.PeriodFromTime(now, s.loc)
	if period.After(current) {
		return map[string]any{"period": period.String(), "goals": items}, nil
	}
	err := db.WithTx(ctx, s.db, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}, func(tx *sql.Tx) error {
		goals, err := s.store.ListGoalsTx(ctx, tx, ownerID, domain.GoalActive, 100)
		if err != nil {
			return err
		}
		monthly, err := s.store.MonthlySpendTx(ctx, tx, ownerID,
			period.Start(s.loc).AddDate(0, -1, 0).UTC(), period.End(s.loc).UTC())
		if err != nil {
			return err
		}
		for i := range goals {
			goal := &goals[i]
			if period.Before(goal.StartPeriod) || (goal.EndPeriod != nil && period.After(*goal.EndPeriod)) {
				continue
			}
			terms, err := s.store.ListTermsTx(ctx, tx, ownerID, goal.ID)
			if err != nil {
				return err
			}
			rows, _ := domain.BuildAchievements(domain.AchievementInput{
				Goal: *goal, Terms: terms, Monthly: monthly, From: period, To: period, Now: now, Loc: s.loc,
			})
			if len(rows) == 0 {
				continue
			}
			row := rows[0]
			items = append(items, map[string]any{
				"goalId": goal.ID, "name": goal.Name, "status": row.Status,
				"baselineFen": row.BaselineFen, "spendFen": row.SpendFen,
				"savedFen": row.SavedFen, "targetSavingFen": row.TargetSavingFen,
				"achievementBps": row.AchievementBps, "onTrack": row.OnTrack,
				"achievedSoFar": row.AchievedSoFar,
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"period": period.String(), "goals": items}, nil
}

func (s *Service) validateScope(ctx context.Context, ownerID string, input GoalScopeInput) (string, []uuid.UUID, error) {
	switch input.Kind {
	case domain.ScopeAll:
		if len(input.CategoryIDs) > 0 {
			return "", nil, apperr.Validation("scope=all 时不能传 categoryIds")
		}
		return domain.ScopeAll, nil, nil
	case domain.ScopeCategories:
		if len(input.CategoryIDs) == 0 {
			return "", nil, apperr.Validation("scope=categories 时至少选择一个分类")
		}
		seen := map[uuid.UUID]struct{}{}
		ids := make([]uuid.UUID, 0, len(input.CategoryIDs))
		for _, categoryID := range input.CategoryIDs {
			if _, ok := seen[categoryID]; ok {
				continue
			}
			seen[categoryID] = struct{}{}
			ok, err := s.store.HasCategory(ctx, ownerID, categoryID)
			if err != nil {
				return "", nil, err
			}
			if !ok {
				return "", nil, apperr.Validation("分类不存在或已归档")
			}
			kind, err := s.store.CategoryKind(ctx, ownerID, categoryID)
			if err != nil {
				return "", nil, err
			}
			if kind != "expense" {
				return "", nil, apperr.Validation("节省目标只能选择支出分类")
			}
			ids = append(ids, categoryID)
		}
		return domain.ScopeCategories, ids, nil
	default:
		return "", nil, apperr.Validation("scope.kind 必须是 all 或 categories")
	}
}

func (s *Service) validateBaseline(input GoalBaselineInput, target int64) error {
	if target < 0 || target > domain.MaxAmountFen {
		return apperr.Validation("目标节省额必须在 0 到 100000000000 分之间")
	}
	switch input.Kind {
	case domain.BaselineFixed:
		if input.BaselineFen == nil || *input.BaselineFen < 0 || *input.BaselineFen > domain.MaxAmountFen {
			return apperr.Validation("固定基准必须提供非负 baselineFen")
		}
		if input.InitialBaselineFen != nil {
			return apperr.Validation("固定基准不能提供 initialBaselineFen")
		}
		if target > *input.BaselineFen {
			return apperr.Validation("目标节省额不能大于固定基准")
		}
	case domain.BaselinePreviousPeriod:
		if input.BaselineFen != nil {
			return apperr.Validation("滚动基准不能提供 baselineFen")
		}
		if input.InitialBaselineFen != nil && target > *input.InitialBaselineFen {
			return apperr.Validation("目标节省额不能大于初始基准")
		}
	default:
		return apperr.Validation("baseline.kind 必须是 fixed 或 previous_period")
	}
	return nil
}

func (s *Service) goalView(goal *domain.SavingGoal, terms []domain.GoalTerm) map[string]any {
	var currentTerm any
	current := domain.PeriodFromTime(time.Now().UTC(), s.loc)
	for i := range terms {
		if !terms[i].EffectiveFromPeriod.After(current) {
			currentTerm = termView(&terms[i])
		}
	}
	return map[string]any{
		"id": goal.ID, "name": goal.Name, "status": goal.Status,
		"startPeriod": goal.StartPeriod.String(), "endPeriod": nullablePeriodString(goal.EndPeriod),
		"version": goal.Version, "currentTerm": currentTerm,
		"createdAt": displayTime(goal.CreatedAt), "updatedAt": displayTime(goal.UpdatedAt),
	}
}

func termView(term *domain.GoalTerm) map[string]any {
	return map[string]any{
		"version": term.Version, "effectiveFromPeriod": term.EffectiveFromPeriod.String(),
		"scope": term.Scope(),
		"baseline": map[string]any{
			"kind": term.BaselineKind, "baselineFen": term.BaselineFen,
			"initialBaselineFen": term.InitialBaselineFen,
		},
		"targetSavingFen": term.TargetSavingFen,
	}
}

func nullablePeriodString(period *domain.Period) any {
	if period == nil {
		return nil
	}
	return period.String()
}

func (s *Service) ArchiveGoal(ctx context.Context, ownerID, actorID string, goalID uuid.UUID, requestID string) error {
	goal, err := s.store.GetGoal(ctx, ownerID, goalID)
	if err != nil {
		return apperr.NotFound("目标不存在")
	}
	if goal.Status == domain.GoalArchived {
		return nil
	}
	status := domain.GoalArchived
	_, err = s.UpdateGoal(ctx, ownerID, actorID, goalID, requestID, UpdateGoalInput{
		Status: &status, Version: goal.Version,
	})
	return err
}
