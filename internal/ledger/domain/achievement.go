package domain

import (
	"sort"
	"time"

	"github.com/google/uuid"
)

type AchievementRow struct {
	Period            string    `json:"period"`
	PeriodStart       time.Time `json:"periodStart"`
	PeriodEnd         time.Time `json:"periodEnd"`
	TermVersion       int       `json:"termVersion"`
	Scope             Scope     `json:"scope"`
	BaselineFen       *int64    `json:"baselineFen"`
	BaselineSource    string    `json:"baselineSource"`
	BaselineAdjusted  bool      `json:"baselineAdjusted"`
	TargetSavingFen   int64     `json:"targetSavingFen"`
	CeilingFen        *int64    `json:"ceilingFen"`
	SpendFen          int64     `json:"spendFen"`
	SavedFen          *int64    `json:"savedFen"`
	GapFen            *int64    `json:"gapFen"`
	AchievementBps    *int      `json:"achievementBps"`
	Status            string    `json:"status"`
	AchievedSoFar     bool      `json:"achievedSoFar"`
	OnTrack           *bool     `json:"onTrack"`
	NetRefund         bool      `json:"netRefund"`
	DaysTotal         int       `json:"daysTotal"`
	DaysElapsed       int       `json:"daysElapsed"`
	ProjectedSpendFen *int64    `json:"projectedSpendFen"`
	ProjectedSavedFen *int64    `json:"projectedSavedFen"`
	ProjectionKind    *string   `json:"projectionKind"`
	AsOf              time.Time `json:"asOf"`
}

type AchievementSummary struct {
	CompletedPeriods      int   `json:"completedPeriods"`
	AchievedPeriods       int   `json:"achievedPeriods"`
	MissedPeriods         int   `json:"missedPeriods"`
	InProgressPeriods     int   `json:"inProgressPeriods"`
	BaselineMissingPeriod int   `json:"baselineMissingPeriods"`
	BaselineTooLowPeriods int   `json:"baselineTooLowPeriods"`
	TotalSavedFen         int64 `json:"totalSavedFen"`
	TotalTargetSavingFen  int64 `json:"totalTargetSavingFen"`
	OverallAchievementBps *int  `json:"overallAchievementBps"`
}

type AchievementInput struct {
	Goal    SavingGoal
	Terms   []GoalTerm
	Monthly []MonthlySpend
	From    Period
	To      Period
	Now     time.Time
	Loc     *time.Location
}

func BuildAchievements(input AchievementInput) ([]AchievementRow, AchievementSummary) {
	loc := input.Loc
	if loc == nil {
		loc = time.UTC
	}
	current := PeriodFromTime(input.Now, loc)
	start := input.From
	if start.Before(input.Goal.StartPeriod) {
		start = input.Goal.StartPeriod
	}
	end := input.To
	if input.Goal.EndPeriod != nil && end.After(*input.Goal.EndPeriod) {
		end = *input.Goal.EndPeriod
	}
	if end.After(current) {
		end = current
	}
	if end.Before(start) {
		return []AchievementRow{}, AchievementSummary{}
	}

	terms := append([]GoalTerm(nil), input.Terms...)
	sort.Slice(terms, func(i, j int) bool {
		return terms[i].EffectiveFromPeriod.Before(terms[j].EffectiveFromPeriod)
	})
	if len(terms) == 0 {
		return []AchievementRow{}, AchievementSummary{}
	}

	type key struct {
		period     Period
		categoryID string
	}
	nets := map[key]int64{}
	counts := map[key]int64{}
	for _, row := range input.Monthly {
		category := ""
		if row.CategoryID != nil {
			category = row.CategoryID.String()
		}
		k := key{period: row.Period, categoryID: category}
		nets[k] += row.NetFen
		counts[k] += row.RecordCount
	}

	termFor := func(period Period) *GoalTerm {
		var found *GoalTerm
		for i := range terms {
			if !terms[i].EffectiveFromPeriod.After(period) {
				found = &terms[i]
			}
		}
		return found
	}

	sumFor := func(period Period, term *GoalTerm) (int64, int64) {
		if term.ScopeKind == ScopeAll {
			var net, count int64
			for k, value := range nets {
				if k.period == period {
					net += value
				}
			}
			for k, value := range counts {
				if k.period == period {
					count += value
				}
			}
			return net, count
		}
		allowed := map[string]struct{}{}
		for _, categoryID := range term.CategoryIDs {
			allowed[categoryID.String()] = struct{}{}
		}
		var net, count int64
		for k, value := range nets {
			if k.period == period {
				if _, ok := allowed[k.categoryID]; ok {
					net += value
				}
			}
		}
		for k, value := range counts {
			if k.period == period {
				if _, ok := allowed[k.categoryID]; ok {
					count += value
				}
			}
		}
		return net, count
	}

	rows := make([]AchievementRow, 0)
	summary := AchievementSummary{}
	for period := start; !period.After(end); period = period.Next() {
		term := termFor(period)
		if term == nil {
			continue
		}
		spend, _ := sumFor(period, term)

		var baseline *int64
		source := "none"
		adjusted := false
		if term.BaselineKind == BaselineFixed && term.BaselineFen != nil {
			value := *term.BaselineFen
			baseline = &value
			source = "fixed"
		} else if term.BaselineKind == BaselinePreviousPeriod {
			previous := period.Prev()
			if previous.Before(input.Goal.StartPeriod) {
				if term.InitialBaselineFen != nil {
					value := *term.InitialBaselineFen
					baseline = &value
					source = "initial"
				}
			} else {
				net, count := sumFor(previous, term)
				if count > 0 {
					if net < 0 {
						net = 0
						adjusted = true
					}
					value := net
					baseline = &value
					source = "previous_period"
				}
			}
		}

		var saved, gap, ceiling *int64
		if baseline != nil {
			value := *baseline - spend
			saved = &value
			gapValue := term.TargetSavingFen - value
			gap = &gapValue
			ceilingValue := *baseline - term.TargetSavingFen
			ceiling = &ceilingValue
		}

		status := StatusInProgress
		if baseline == nil {
			status = StatusBaselineMiss
		} else if *baseline < term.TargetSavingFen {
			status = StatusBaselineLow
		} else if period.Before(current) {
			if saved != nil && *saved >= term.TargetSavingFen {
				status = StatusAchieved
			} else {
				status = StatusMissed
			}
		}

		achievedSoFar := saved != nil && *saved >= term.TargetSavingFen
		var bps *int
		if status != StatusBaselineMiss && status != StatusBaselineLow && saved != nil {
			bps = BPS(*saved, term.TargetSavingFen)
		}

		daysTotal := period.Days(loc)
		daysElapsed := daysTotal
		if !period.Before(current) {
			daysElapsed = input.Now.In(loc).Day()
			if daysElapsed < 1 {
				daysElapsed = 1
			}
			if daysElapsed > daysTotal {
				daysElapsed = daysTotal
			}
		}

		var projectedSpend, projectedSaved *int64
		var onTrack *bool
		var projectionKind *string
		if period == current && status == StatusInProgress && daysElapsed >= 7 && term.TargetSavingFen > 0 && baseline != nil {
			projected := RoundHalfAwayFromZero(spend*int64(daysTotal), int64(daysElapsed))
			savedProjection := *baseline - projected
			track := savedProjection >= term.TargetSavingFen
			kind := "linear"
			projectedSpend = &projected
			projectedSaved = &savedProjection
			onTrack = &track
			projectionKind = &kind
		}

		rows = append(rows, AchievementRow{
			Period:            period.String(),
			PeriodStart:       period.Start(loc),
			PeriodEnd:         period.End(loc),
			TermVersion:       term.Version,
			Scope:             term.Scope(),
			BaselineFen:       baseline,
			BaselineSource:    source,
			BaselineAdjusted:  adjusted,
			TargetSavingFen:   term.TargetSavingFen,
			CeilingFen:        ceiling,
			SpendFen:          spend,
			SavedFen:          saved,
			GapFen:            gap,
			AchievementBps:    bps,
			Status:            status,
			AchievedSoFar:     achievedSoFar,
			OnTrack:           onTrack,
			NetRefund:         spend < 0,
			DaysTotal:         daysTotal,
			DaysElapsed:       daysElapsed,
			ProjectedSpendFen: projectedSpend,
			ProjectedSavedFen: projectedSaved,
			ProjectionKind:    projectionKind,
			AsOf:              input.Now.In(loc),
		})

		if period.Before(current) {
			summary.CompletedPeriods++
			switch status {
			case StatusAchieved:
				summary.AchievedPeriods++
			case StatusMissed:
				summary.MissedPeriods++
			case StatusBaselineMiss:
				summary.BaselineMissingPeriod++
			case StatusBaselineLow:
				summary.BaselineTooLowPeriods++
			}
			if status == StatusAchieved || status == StatusMissed {
				summary.TotalSavedFen += *saved
				summary.TotalTargetSavingFen += term.TargetSavingFen
			}
		} else {
			summary.InProgressPeriods++
		}
	}
	if summary.TotalTargetSavingFen > 0 {
		summary.OverallAchievementBps = BPS(summary.TotalSavedFen, summary.TotalTargetSavingFen)
	}
	return rows, summary
}

func PeriodsBetween(from, to Period) []Period {
	var periods []Period
	for p := from; !p.After(to); p = p.Next() {
		periods = append(periods, p)
	}
	return periods
}

func CategoryIDString(id *uuid.UUID) string {
	if id == nil {
		return ""
	}
	return id.String()
}
