package domain

import (
	"fmt"
	"strings"
	"time"
)

type Period struct {
	Year  int
	Month time.Month
}

func NewPeriod(year int, month time.Month) Period {
	return Period{Year: year, Month: month}
}

func ParsePeriod(value string) (Period, error) {
	if len(value) != 7 || value[4] != '-' {
		return Period{}, fmt.Errorf("period must be YYYY-MM")
	}
	t, err := time.Parse("2006-01", value)
	if err != nil {
		return Period{}, fmt.Errorf("period must be YYYY-MM")
	}
	return Period{Year: t.Year(), Month: t.Month()}, nil
}

func PeriodFromTime(t time.Time, loc *time.Location) Period {
	local := t.In(loc)
	return Period{Year: local.Year(), Month: local.Month()}
}

func PeriodFromDate(t time.Time) Period {
	return Period{Year: t.Year(), Month: t.Month()}
}

func (p Period) String() string {
	return fmt.Sprintf("%04d-%02d", p.Year, int(p.Month))
}

func (p Period) Start(loc *time.Location) time.Time {
	return time.Date(p.Year, p.Month, 1, 0, 0, 0, 0, loc)
}

func (p Period) End(loc *time.Location) time.Time {
	return p.Start(loc).AddDate(0, 1, 0)
}

func (p Period) Next() Period {
	if p.Month == time.December {
		return Period{Year: p.Year + 1, Month: time.January}
	}
	return Period{Year: p.Year, Month: p.Month + 1}
}

func (p Period) Prev() Period {
	if p.Month == time.January {
		return Period{Year: p.Year - 1, Month: time.December}
	}
	return Period{Year: p.Year, Month: p.Month - 1}
}

func (p Period) AddMonths(months int) Period {
	t := time.Date(p.Year, p.Month, 1, 0, 0, 0, 0, time.UTC).AddDate(0, months, 0)
	return Period{Year: t.Year(), Month: t.Month()}
}

func (p Period) Days(loc *time.Location) int {
	return p.End(loc).AddDate(0, 0, -1).Day()
}

func (p Period) Compare(other Period) int {
	if p.Year != other.Year {
		if p.Year < other.Year {
			return -1
		}
		return 1
	}
	if p.Month < other.Month {
		return -1
	}
	if p.Month > other.Month {
		return 1
	}
	return 0
}

func (p Period) Before(other Period) bool { return p.Compare(other) < 0 }
func (p Period) After(other Period) bool  { return p.Compare(other) > 0 }

func ParseDate(value string, loc *time.Location) (time.Time, error) {
	t, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(value), loc)
	if err != nil {
		return time.Time{}, fmt.Errorf("date must be YYYY-MM-DD")
	}
	return t, nil
}

func StartOfMonth(t time.Time, loc *time.Location) time.Time {
	local := t.In(loc)
	return time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, loc)
}
