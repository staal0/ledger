package app

import "testing"

func TestParseMoney(t *testing.T) {
	tests := []struct {
		in, dec, thou string
		want          Money
		ok            bool
	}{
		{"", ",", ".", 0, true},
		{"12500", ",", ".", 1250000, true},
		{"12.500", ",", ".", 1250000, true},
		{"1.234.567,89", ",", ".", 123456789, true},
		{"-1.200,5", ",", ".", -120050, true},
		{",5", ",", ".", 50, true},
		{"1,234.56", ".", ",", 123456, true},
		{"12.5", ",", ".", 0, false},     // a misplaced separator is never a decimal
		{"1.23.456", ",", ".", 0, false}, // groups after the first must be three digits
		{"1234.567", ",", ".", 0, false}, // first group at most three digits
		{".500", ",", ".", 0, false},
		{"12,345", ",", ".", 0, false}, // three decimals
		{"12a", ",", ".", 0, false},
		{"9999999999,99", ",", ".", 999999999999, true},
		{"10000000000", ",", ".", 0, false}, // more than DECIMAL(12,2) holds
		{"-", ",", ".", 0, true},            // what the browser accepts as an unfinished entry
		{"1234.56", ".", "", 123456, true},  // database values: no thousands separator
	}
	for _, tt := range tests {
		got, err := ParseMoney(tt.in, tt.dec, tt.thou)
		if (err == nil) != tt.ok || got != tt.want {
			t.Errorf("parseMoney(%q, %q, %q) = %d, %v; want %d, ok=%v", tt.in, tt.dec, tt.thou, got, err, tt.want, tt.ok)
		}
	}
}

func TestFormatRoundTrip(t *testing.T) {
	for _, m := range []Money{0, 5, 100, 123456, -98765432, 100000000} {
		for _, f := range numberFormats {
			s := m.Format(f[0], f[1])
			got, err := ParseMoney(s, f[0], f[1])
			if err != nil || got != m {
				t.Errorf("%d formatted as %q parsed back as %d, %v", m, s, got, err)
			}
		}
	}
}
