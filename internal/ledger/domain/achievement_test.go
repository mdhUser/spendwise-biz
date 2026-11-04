package domain

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestPreviousPeriodBaselineNormalPath(t *testing.T) {
	loc := mustLocation(t)
	goal := SavingGoal{
		ID: uuid.New(), Name: "rolling", Status: GoalActive,
		StartPeriod: ParsePeriodOrFail(t, "2026-01"), Version: 1,
	}
	term := GoalTerm{
		ID: uuid.New(), GoalID: goal.ID, Version: 1,
		EffectiveFromPeriod: ParsePeriodOrFail(t, "2026-01"),
		ScopeKind:           ScopeAll, BaselineKind: BaselinePreviousPeriod,
		TargetSavingFen: 30000,
	}
	rows, summary := BuildAchievements(AchievementInput{
		Goal: goal, Terms: []GoalTerm{term},
		Monthly: []MonthlySpend{
			{Period: ParsePeriodOrFail(t, "2026-01"), NetFen: 280000, RecordCount: 1},
			{Period: ParsePeriodOrFail(t, "2026-02"), NetFen: 230000, RecordCount: 1},
		},
		From: ParsePeriodOrFail(t, "2026-02"), To: ParsePeriodOrFail(t, "2026-02"),
		Now: time.Date(2026, 2, 20, 12, 0, 0, 0, loc), Loc: loc,
	})
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	row := rows[0]
	if row.BaselineSource != "previous_period" || row.BaselineFen == nil || *row.BaselineFen != 280000 {
		t.Fatalf("baseline = %+v, want previous_period 280000", row)
	}
	if row.BaselineAdjusted {
		t.Fatalf("positive previous baseline should not be adjusted: %+v", row)
	}
	if row.SpendFen != 230000 || row.SavedFen == nil || *row.SavedFen != 50000 {
		t.Fatalf("spend/saved = %d/%v, want 230000/50000", row.SpendFen, row.SavedFen)
	}
	if row.GapFen == nil || *row.GapFen != -20000 {
		t.Fatalf("gap = %v, want -20000", row.GapFen)
	}
	if row.CeilingFen == nil || *row.CeilingFen != 250000 {
		t.Fatalf("ceiling = %v, want 250000", row.CeilingFen)
	}
	if row.Status != StatusInProgress || !row.AchievedSoFar {
		t.Fatalf("status = %s achievedSoFar = %v, want in_progress/true", row.Status, row.AchievedSoFar)
	}
	if row.AchievementBps == nil || *row.AchievementBps != 16667 {
		t.Fatalf("bps = %v, want 16667", row.AchievementBps)
	}
	if summary.InProgressPeriods != 1 || summary.CompletedPeriods != 0 {
		t.Fatalf("summary = %+v", summary)
	}
}

func TestPreviousPeriodNegativeBaselineClampedToZero(t *testing.T) {
	loc := mustLocation(t)
	goal := SavingGoal{
		ID: uuid.New(), Name: "refund-heavy", Status: GoalActive,
		StartPeriod: ParsePeriodOrFail(t, "2026-01"), Version: 1,
	}
	term := GoalTerm{
		ID: uuid.New(), GoalID: goal.ID, Version: 1,
		EffectiveFromPeriod: ParsePeriodOrFail(t, "2026-01"),
		ScopeKind:           ScopeAll, BaselineKind: BaselinePreviousPeriod,
		TargetSavingFen: 30000,
	}
	rows, _ := BuildAchievements(AchievementInput{
		Goal: goal, Terms: []GoalTerm{term},
		Monthly: []MonthlySpend{
			{Period: ParsePeriodOrFail(t, "2026-01"), NetFen: -5000, RecordCount: 1},
			{Period: ParsePeriodOrFail(t, "2026-02"), NetFen: 10000, RecordCount: 1},
		},
		From: ParsePeriodOrFail(t, "2026-02"), To: ParsePeriodOrFail(t, "2026-02"),
		Now: time.Date(2026, 2, 20, 12, 0, 0, 0, loc), Loc: loc,
	})
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	row := rows[0]
	if !row.BaselineAdjusted || row.BaselineFen == nil || *row.BaselineFen != 0 {
		t.Fatalf("baseline = %v adjusted = %v, want 0/true", row.BaselineFen, row.BaselineAdjusted)
	}
	if row.BaselineSource != "previous_period" {
		t.Fatalf("baseline source = %q, want previous_period", row.BaselineSource)
	}
	if row.Status != StatusBaselineLow {
		t.Fatalf("status = %q, want baseline_too_low", row.Status)
	}
	if row.SavedFen == nil || *row.SavedFen != -10000 {
		t.Fatalf("saved = %v, want -10000", row.SavedFen)
	}
}

func TestZeroTargetSaving(t *testing.T) {
	loc := mustLocation(t)
	goal := SavingGoal{
		ID: uuid.New(), Name: "zero target", Status: GoalActive,
		StartPeriod: ParsePeriodOrFail(t, "2026-01"), Version: 1,
	}
	term := GoalTerm{
		ID: uuid.New(), GoalID: goal.ID, Version: 1,
		EffectiveFromPeriod: ParsePeriodOrFail(t, "2026-01"),
		ScopeKind:           ScopeAll, BaselineKind: BaselineFixed,
		BaselineFen: int64Ptr(300000), TargetSavingFen: 0,
	}
	rows, summary := BuildAchievements(AchievementInput{
		Goal: goal, Terms: []GoalTerm{term},
		Monthly: []MonthlySpend{{Period: ParsePeriodOrFail(t, "2026-01"), NetFen: 100000, RecordCount: 1}},
		From:    ParsePeriodOrFail(t, "2026-01"), To: ParsePeriodOrFail(t, "2026-01"),
		Now: time.Date(2026, 2, 10, 12, 0, 0, 0, loc), Loc: loc,
	})
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	row := rows[0]
	if row.Status != StatusAchieved || row.SavedFen == nil || *row.SavedFen != 200000 {
		t.Fatalf("row = %+v, want achieved with 200000 saved", row)
	}
	if row.AchievementBps != nil {
		t.Fatalf("bps = %v, want nil for zero target", row.AchievementBps)
	}
	if summary.TotalTargetSavingFen != 0 || summary.TotalSavedFen != 200000 || summary.OverallAchievementBps != nil {
		t.Fatalf("summary = %+v, want totals without overall bps", summary)
	}
}

func TestCategoryScopeFiltersSpending(t *testing.T) {
	loc := mustLocation(t)
	catA, catB, catC := uuid.New(), uuid.New(), uuid.New()
	goal := SavingGoal{
		ID: uuid.New(), Name: "categories", Status: GoalActive,
		StartPeriod: ParsePeriodOrFail(t, "2026-02"), Version: 1,
	}
	term := GoalTerm{
		ID: uuid.New(), GoalID: goal.ID, Version: 1,
		EffectiveFromPeriod: ParsePeriodOrFail(t, "2026-02"),
		ScopeKind:           ScopeCategories, CategoryIDs: []uuid.UUID{catA, catB},
		BaselineKind: BaselineFixed, BaselineFen: int64Ptr(300000), TargetSavingFen: 50000,
	}
	rows, _ := BuildAchievements(AchievementInput{
		Goal: goal, Terms: []GoalTerm{term},
		Monthly: []MonthlySpend{
			{Period: ParsePeriodOrFail(t, "2026-02"), CategoryID: &catA, NetFen: 50000, RecordCount: 1},
			{Period: ParsePeriodOrFail(t, "2026-02"), CategoryID: &catB, NetFen: 20000, RecordCount: 2},
			{Period: ParsePeriodOrFail(t, "2026-02"), CategoryID: &catC, NetFen: 999000, RecordCount: 3},
			{Period: ParsePeriodOrFail(t, "2026-02"), NetFen: 7000, RecordCount: 1},
		},
		From: ParsePeriodOrFail(t, "2026-02"), To: ParsePeriodOrFail(t, "2026-02"),
		Now: time.Date(2026, 2, 20, 12, 0, 0, 0, loc), Loc: loc,
	})
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	row := rows[0]
	if row.Scope.Kind != ScopeCategories || len(row.Scope.CategoryIDs) != 2 {
		t.Fatalf("scope = %+v", row.Scope)
	}
	if row.SpendFen != 70000 {
		t.Fatalf("spend = %d, want 70000 (only selected categories)", row.SpendFen)
	}
	if row.SavedFen == nil || *row.SavedFen != 230000 {
		t.Fatalf("saved = %v, want 230000", row.SavedFen)
	}
	if row.AchievementBps == nil || *row.AchievementBps != 46000 {
		t.Fatalf("bps = %v, want 46000", row.AchievementBps)
	}
}

func TestTermsSwitchAcrossPeriods(t *testing.T) {
	loc := mustLocation(t)
	goal := SavingGoal{
		ID: uuid.New(), Name: "switching terms", Status: GoalActive,
		StartPeriod: ParsePeriodOrFail(t, "2026-01"), Version: 1,
	}
	term1 := GoalTerm{
		ID: uuid.New(), GoalID: goal.ID, Version: 1,
		EffectiveFromPeriod: ParsePeriodOrFail(t, "2026-01"),
		ScopeKind:           ScopeAll, BaselineKind: BaselineFixed,
		BaselineFen: int64Ptr(300000), TargetSavingFen: 50000,
	}
	term2 := GoalTerm{
		ID: uuid.New(), GoalID: goal.ID, Version: 2,
		EffectiveFromPeriod: ParsePeriodOrFail(t, "2026-03"),
		ScopeKind:           ScopeAll, BaselineKind: BaselineFixed,
		BaselineFen: int64Ptr(200000), TargetSavingFen: 40000,
	}
	monthly := make([]MonthlySpend, 0, 5)
	for _, month := range []string{"2026-01", "2026-02", "2026-03", "2026-04", "2026-05"} {
		monthly = append(monthly, MonthlySpend{Period: ParsePeriodOrFail(t, month), NetFen: 100000, RecordCount: 1})
	}
	rows, _ := BuildAchievements(AchievementInput{
		Goal: goal, Terms: []GoalTerm{term2, term1}, Monthly: monthly,
		From: ParsePeriodOrFail(t, "2026-01"), To: ParsePeriodOrFail(t, "2026-05"),
		Now: time.Date(2026, 5, 20, 12, 0, 0, 0, loc), Loc: loc,
	})
	if len(rows) != 5 {
		t.Fatalf("rows = %d, want 5", len(rows))
	}
	wantVersions := []int{1, 1, 2, 2, 2}
	wantTargets := []int64{50000, 50000, 40000, 40000, 40000}
	wantBaselines := []int64{300000, 300000, 200000, 200000, 200000}
	for i, row := range rows {
		if row.TermVersion != wantVersions[i] {
			t.Fatalf("row %d term version = %d, want %d", i, row.TermVersion, wantVersions[i])
		}
		if row.TargetSavingFen != wantTargets[i] {
			t.Fatalf("row %d target = %d, want %d", i, row.TargetSavingFen, wantTargets[i])
		}
		if row.BaselineFen == nil || *row.BaselineFen != wantBaselines[i] {
			t.Fatalf("row %d baseline = %v, want %d", i, row.BaselineFen, wantBaselines[i])
		}
	}
}

func TestOverallAchievementBps(t *testing.T) {
	loc := mustLocation(t)
	goal := SavingGoal{
		ID: uuid.New(), Name: "overall", Status: GoalActive,
		StartPeriod: ParsePeriodOrFail(t, "2026-01"), Version: 1,
	}
	term := GoalTerm{
		ID: uuid.New(), GoalID: goal.ID, Version: 1,
		EffectiveFromPeriod: ParsePeriodOrFail(t, "2026-01"),
		ScopeKind:           ScopeAll, BaselineKind: BaselineFixed,
		BaselineFen: int64Ptr(300000), TargetSavingFen: 50000,
	}
	rows, summary := BuildAchievements(AchievementInput{
		Goal: goal, Terms: []GoalTerm{term},
		Monthly: []MonthlySpend{
			{Period: ParsePeriodOrFail(t, "2026-01"), NetFen: 230000, RecordCount: 1},
			{Period: ParsePeriodOrFail(t, "2026-02"), NetFen: 270000, RecordCount: 1},
		},
		From: ParsePeriodOrFail(t, "2026-01"), To: ParsePeriodOrFail(t, "2026-02"),
		Now: time.Date(2026, 3, 10, 12, 0, 0, 0, loc), Loc: loc,
	})
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if rows[0].Status != StatusAchieved || rows[1].Status != StatusMissed {
		t.Fatalf("statuses = %s/%s, want achieved/missed", rows[0].Status, rows[1].Status)
	}
	if summary.CompletedPeriods != 2 || summary.AchievedPeriods != 1 || summary.MissedPeriods != 1 {
		t.Fatalf("summary counts = %+v", summary)
	}
	if summary.TotalSavedFen != 100000 || summary.TotalTargetSavingFen != 100000 {
		t.Fatalf("summary totals = %+v", summary)
	}
	if summary.OverallAchievementBps == nil || *summary.OverallAchievementBps != 10000 {
		t.Fatalf("overall bps = %v, want 10000", summary.OverallAchievementBps)
	}
}

func TestAchievementRangeClampedToGoalEndPeriod(t *testing.T) {
	loc := mustLocation(t)
	end := ParsePeriodOrFail(t, "2026-03")
	goal := SavingGoal{
		ID: uuid.New(), Name: "bounded", Status: GoalActive,
		StartPeriod: ParsePeriodOrFail(t, "2026-02"), EndPeriod: &end, Version: 1,
	}
	term := GoalTerm{
		ID: uuid.New(), GoalID: goal.ID, Version: 1,
		EffectiveFromPeriod: ParsePeriodOrFail(t, "2026-02"),
		ScopeKind:           ScopeAll, BaselineKind: BaselineFixed,
		BaselineFen: int64Ptr(300000), TargetSavingFen: 50000,
	}
	rows, _ := BuildAchievements(AchievementInput{
		Goal: goal, Terms: []GoalTerm{term},
		Monthly: []MonthlySpend{
			{Period: ParsePeriodOrFail(t, "2026-02"), NetFen: 100000, RecordCount: 1},
			{Period: ParsePeriodOrFail(t, "2026-03"), NetFen: 100000, RecordCount: 1},
			{Period: ParsePeriodOrFail(t, "2026-04"), NetFen: 100000, RecordCount: 1},
		},
		From: ParsePeriodOrFail(t, "2026-01"), To: ParsePeriodOrFail(t, "2026-05"),
		Now: time.Date(2026, 5, 10, 12, 0, 0, 0, loc), Loc: loc,
	})
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 (clamped to goal end period)", len(rows))
	}
	if rows[0].Period != "2026-02" || rows[1].Period != "2026-03" {
		t.Fatalf("periods = %s/%s, want 2026-02/2026-03", rows[0].Period, rows[1].Period)
	}
}

func TestAchievementsEmptyRowsAreNonNilSlices(t *testing.T) {
	loc := mustLocation(t)
	goal := SavingGoal{
		ID: uuid.New(), Name: "empty", Status: GoalActive,
		StartPeriod: ParsePeriodOrFail(t, "2026-03"), Version: 1,
	}
	term := GoalTerm{
		ID: uuid.New(), GoalID: goal.ID, Version: 1,
		EffectiveFromPeriod: ParsePeriodOrFail(t, "2026-03"),
		ScopeKind:           ScopeAll, BaselineKind: BaselineFixed,
		BaselineFen: int64Ptr(300000), TargetSavingFen: 50000,
	}
	rows, summary := BuildAchievements(AchievementInput{
		Goal: goal, Terms: []GoalTerm{term},
		From: ParsePeriodOrFail(t, "2026-01"), To: ParsePeriodOrFail(t, "2026-02"),
		Now: time.Date(2026, 3, 10, 12, 0, 0, 0, loc), Loc: loc,
	})
	if rows == nil || len(rows) != 0 {
		t.Fatalf("rows = %#v, want empty non-nil slice", rows)
	}
	if summary != (AchievementSummary{}) {
		t.Fatalf("summary = %+v, want zero value", summary)
	}
	raw, err := json.Marshal(rows)
	if err != nil {
		t.Fatalf("marshal rows: %v", err)
	}
	if string(raw) != "[]" {
		t.Fatalf("marshaled rows = %s, want []", raw)
	}

	rows, _ = BuildAchievements(AchievementInput{
		Goal: goal, Terms: nil,
		From: ParsePeriodOrFail(t, "2026-03"), To: ParsePeriodOrFail(t, "2026-03"),
		Now: time.Date(2026, 3, 10, 12, 0, 0, 0, loc), Loc: loc,
	})
	if rows == nil || len(rows) != 0 {
		t.Fatalf("rows without terms = %#v, want empty non-nil slice", rows)
	}
}
