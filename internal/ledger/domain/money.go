package domain

const MaxAmountFen int64 = 100_000_000_000

func ValidAmount(fen int64) bool {
	return fen > 0 && fen <= MaxAmountFen
}

// RoundHalfAwayFromZero divides n by d with integer arithmetic.
func RoundHalfAwayFromZero(n, d int64) int64 {
	if d == 0 {
		return 0
	}
	negative := (n < 0) != (d < 0)
	if n < 0 {
		n = -n
	}
	if d < 0 {
		d = -d
	}
	q := n / d
	r := n % d
	if r*2 >= d {
		q++
	}
	if negative {
		return -q
	}
	return q
}

// BPS returns saved/target in basis points, rounded away from zero.
// A nil result means undefined (target is zero).
func BPS(saved, target int64) *int {
	if target == 0 {
		return nil
	}
	value := RoundHalfAwayFromZero(saved*10000, target)
	result := int(value)
	return &result
}
