package domain

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestPeriodBasics(t *testing.T) {
	period, err := ParsePeriod("2026-02")
	if err != nil {
		t.Fatal(err)
	}
	if period.String() != "2026-02" {
		t.Fatalf("unexpected period %s", period)
	}
	loc, _ := time.LoadLocation("Asia/Shanghai")
	if got := period.Days(loc); got != 28 {
		t.Fatalf("2026-02 days = %d", got)
	}
	leap, _ := ParsePeriod("2024-02")
	if got := leap.Days(loc); got != 29 {
		t.Fatalf("2024-02 days = %d", got)
	}
	if next := ParsePeriodOrFail(t, "2026-12").Next(); next.String() != "2027-01" {
		t.Fatalf("december next = %s", next)
	}
	if prev := ParsePeriodOrFail(t, "2026-01").Prev(); prev.String() != "2025-12" {
		t.Fatalf("january prev = %s", prev)
	}
	if got := ParsePeriodOrFail(t, "2026-11").AddMonths(3).String(); got != "2027-02" {
		t.Fatalf("add months = %s", got)
	}
	if _, err := ParsePeriod("2026-13"); err == nil {
		t.Fatal("expected invalid month error")
	}
}

func ParsePeriodOrFail(t *testing.T, value string) Period {
	t.Helper()
	period, err := ParsePeriod(value)
	if err != nil {
		t.Fatal(err)
	}
	return period
}

func TestMoneyRounding(t *testing.T) {
	cases := []struct {
		n, d, want int64
	}{
		{5, 2, 3}, {-5, 2, -3}, {4, 2, 2}, {-4, 2, -2}, {1, 3, 0}, {-1, 3, 0},
	}
	for _, tc := range cases {
		if got := RoundHalfAwayFromZero(tc.n, tc.d); got != tc.want {
			t.Fatalf("round(%d,%d)=%d want %d", tc.n, tc.d, got, tc.want)
		}
	}
	if got := BPS(70000, 50000); got == nil || *got != 14000 {
		t.Fatalf("bps = %v", got)
	}
	if got := BPS(20000, 30000); got == nil || *got != 6667 {
		t.Fatalf("bps = %v", got)
	}
	if got := BPS(100, 0); got != nil {
		t.Fatalf("zero target bps = %v", got)
	}
}

func int64Ptr(value int64) *int64 { return &value }

func TestFixedBaselineAchievement(t *testing.T) {
	loc := mustLocation(t)
	goal := SavingGoal{
		ID: uuid.New(), Name: "fixed", Status: GoalActive,
		StartPeriod: ParsePeriodOrFail(t, "2026-10"), Version: 1,
	}
	term := GoalTerm{
		ID: uuid.New(), GoalID: goal.ID, Version: 1,
		EffectiveFromPeriod: ParsePeriodOrFail(t, "2026-10"),
		ScopeKind:           ScopeAll, BaselineKind: BaselineFixed,
		BaselineFen: int64Ptr(300000), TargetSavingFen: 50000,
	}
	monthly := []MonthlySpend{
		{Period: ParsePeriodOrFail(t, "2026-10"), NetFen: 230000, RecordCount: 1},
	}
	rows, summary := BuildAchievements(AchievementInput{
		Goal: goal, Terms: []GoalTerm{term}, Monthly: monthly,
		From: ParsePeriodOrFail(t, "2026-10"), To: ParsePeriodOrFail(t, "2026-11"),
		Now: time.Date(2026, 11, 4, 12, 0, 0, 0, loc), Loc: loc,
	})
	if len(rows) != 2 {
		t.Fatalf("rows = %d", len(rows))
	}
	october := rows[0]
	if october.Status != StatusAchieved || october.SavedFen == nil || *october.SavedFen != 70000 {
		t.Fatalf("october row = %+v", october)
	}
	if october.AchievementBps == nil || *october.AchievementBps != 14000 {
		t.Fatalf("october bps = %v", october.AchievementBps)
	}
	november := rows[1]
	if november.Status != StatusInProgress || !november.AchievedSoFar {
		t.Fatalf("november row = %+v", november)
	}
	if summary.CompletedPeriods != 1 || summary.AchievedPeriods != 1 || summary.TotalSavedFen != 70000 {
		t.Fatalf("summary = %+v", summary)
	}
}

func TestRollingBaselineAndProjection(t *testing.T) {
	loc := mustLocation(t)
	goal := SavingGoal{
		ID: uuid.New(), Name: "rolling", Status: GoalActive,
		StartPeriod: ParsePeriodOrFail(t, "2026-11"), Version: 1,
	}
	term := GoalTerm{
		ID: uuid.New(), GoalID: goal.ID, Version: 1,
		EffectiveFromPeriod: ParsePeriodOrFail(t, "2026-11"),
		ScopeKind:           ScopeAll, BaselineKind: BaselinePreviousPeriod,
		InitialBaselineFen: int64Ptr(280000), TargetSavingFen: 30000,
	}
	rows, summary := BuildAchievements(AchievementInput{
		Goal: goal, Terms: []GoalTerm{term},
		Monthly: []MonthlySpend{{Period: ParsePeriodOrFail(t, "2026-11"), NetFen: 230000, RecordCount: 1}},
		From:    ParsePeriodOrFail(t, "2026-11"), To: ParsePeriodOrFail(t, "2026-11"),
		Now: time.Date(2026, 11, 20, 12, 0, 0, 0, loc), Loc: loc,
	})
	if len(rows) != 1 {
		t.Fatalf("rows = %d", len(rows))
	}
	row := rows[0]
	if row.BaselineSource != "initial" || row.BaselineFen == nil || *row.BaselineFen != 280000 {
		t.Fatalf("baseline = %+v", row)
	}
	if row.SavedFen == nil || *row.SavedFen != 50000 {
		t.Fatalf("saved = %v", row.SavedFen)
	}
	if row.AchievementBps == nil || *row.AchievementBps != 16667 {
		t.Fatalf("bps = %v", row.AchievementBps)
	}
	if row.DaysElapsed != 20 || row.ProjectedSpendFen == nil || *row.ProjectedSpendFen != 345000 {
		t.Fatalf("projection = %+v", row)
	}
	if row.OnTrack == nil || *row.OnTrack {
		t.Fatalf("onTrack = %v", row.OnTrack)
	}
	if summary.InProgressPeriods != 1 {
		t.Fatalf("summary = %+v", summary)
	}
}

func TestBaselineMissing(t *testing.T) {
	loc := mustLocation(t)
	goal := SavingGoal{
		ID: uuid.New(), Name: "missing", Status: GoalActive,
		StartPeriod: ParsePeriodOrFail(t, "2026-11"), Version: 1,
	}
	term := GoalTerm{
		ID: uuid.New(), GoalID: goal.ID, Version: 1,
		EffectiveFromPeriod: ParsePeriodOrFail(t, "2026-11"),
		ScopeKind:           ScopeAll, BaselineKind: BaselinePreviousPeriod,
		TargetSavingFen: 30000,
	}
	rows, _ := BuildAchievements(AchievementInput{
		Goal: goal, Terms: []GoalTerm{term},
		From: ParsePeriodOrFail(t, "2026-11"), To: ParsePeriodOrFail(t, "2026-11"),
		Now: time.Date(2026, 11, 20, 12, 0, 0, 0, loc), Loc: loc,
	})
	if len(rows) != 1 || rows[0].Status != StatusBaselineMiss || rows[0].BaselineFen != nil || rows[0].SavedFen != nil {
		t.Fatalf("row = %+v", rows)
	}
}

func TestBaselineTooLow(t *testing.T) {
	loc := mustLocation(t)
	goal := SavingGoal{
		ID: uuid.New(), Name: "low", Status: GoalActive,
		StartPeriod: ParsePeriodOrFail(t, "2026-10"), Version: 1,
	}
	term := GoalTerm{
		ID: uuid.New(), GoalID: goal.ID, Version: 1,
		EffectiveFromPeriod: ParsePeriodOrFail(t, "2026-10"),
		ScopeKind:           ScopeAll, BaselineKind: BaselinePreviousPeriod,
		TargetSavingFen: 50000,
	}
	rows, _ := BuildAchievements(AchievementInput{
		Goal: goal, Terms: []GoalTerm{term},
		Monthly: []MonthlySpend{{Period: ParsePeriodOrFail(t, "2026-10"), NetFen: 40000, RecordCount: 1}},
		From:    ParsePeriodOrFail(t, "2026-11"), To: ParsePeriodOrFail(t, "2026-11"),
		Now: time.Date(2026, 11, 20, 12, 0, 0, 0, loc), Loc: loc,
	})
	if len(rows) != 1 || rows[0].Status != StatusBaselineLow || rows[0].AchievementBps != nil {
		t.Fatalf("row = %+v", rows)
	}
	if rows[0].BaselineFen == nil || *rows[0].BaselineFen != 40000 {
		t.Fatalf("baseline = %v", rows[0].BaselineFen)
	}
}

func mustLocation(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}
