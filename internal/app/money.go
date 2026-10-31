package app

import (
	"database/sql/driver"
	"fmt"
	"strconv"
	"strings"
)

// Money is an amount in hundredths (øre/cents), so sums are exact.
type Money int64

func (m *Money) Scan(src any) error {
	var err error
	switch v := src.(type) {
	case []byte:
		*m, err = ParseMoney(string(v), ".", "")
	case string:
		*m, err = ParseMoney(v, ".", "")
	case int64:
		*m = Money(v * 100)
	case nil:
		*m = 0
	default:
		err = fmt.Errorf("money: cannot scan %T", src)
	}
	return err
}

func (m Money) Value() (driver.Value, error) { return m.Format(".", ""), nil }

// maxWhole matches the DECIMAL(12,2) amount column.
const maxWhole = 10

// ParseMoney accepts an optional minus, up to maxWhole digits and up to two decimals.
// Thousands separators are only accepted in proper groups of three, so with
// "." as separator "12.500" is 12500 and "12.5" is an error, never 12.5.
func ParseMoney(s, decimal, thousands string) (Money, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "-" { // a lone minus is an unfinished entry, like an empty field
		return 0, nil
	}
	neg := strings.HasPrefix(s, "-")
	whole, frac, _ := strings.Cut(strings.TrimPrefix(s, "-"), decimal)
	if thousands != "" && strings.Contains(whole, thousands) {
		groups := strings.Split(whole, thousands)
		for i, g := range groups {
			if len(g) > 3 || len(g) < 3 && (i > 0 || g == "") {
				return 0, fmt.Errorf("invalid amount %q", s)
			}
		}
		whole = strings.Join(groups, "")
	}
	if len(frac) > 2 || len(strings.TrimLeft(whole, "0")) > maxWhole || whole+frac == "" || strings.Trim(whole+frac, "0123456789") != "" {
		return 0, fmt.Errorf("invalid amount %q", s)
	}
	w, err := strconv.ParseInt("0"+whole, 10, 64)
	if err != nil {
		return 0, err
	}
	f, _ := strconv.Atoi((frac + "00")[:2])
	m := Money(w*100 + int64(f))
	if neg {
		m = -m
	}
	return m, nil
}

// Format writes m with the given separators; decimals are only shown when non-zero.
func (m Money) Format(decimal, thousands string) string {
	neg := m < 0
	if neg {
		m = -m
	}
	s := strconv.FormatInt(int64(m/100), 10)
	if thousands != "" {
		for i := len(s) - 3; i > 0; i -= 3 {
			s = s[:i] + thousands + s[i:]
		}
	}
	if c := m % 100; c != 0 {
		s += fmt.Sprintf("%s%02d", decimal, int64(c))
	}
	if neg {
		s = "-" + s
	}
	return s
}
