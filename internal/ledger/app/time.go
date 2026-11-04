package app

import "time"

var displayLocation = func() *time.Location {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.FixedZone("CST", 8*60*60)
	}
	return loc
}()

func displayTime(value time.Time) string {
	return value.In(displayLocation).Format(time.RFC3339)
}

func displayTimePtr(value *time.Time) any {
	if value == nil {
		return nil
	}
	return displayTime(*value)
}
