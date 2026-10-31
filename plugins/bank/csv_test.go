package bank

import (
	"errors"
	"testing"
)

// A made-up export in the Lån & Spar layout: a byte order mark, CRLF, no
// header, newest first and two identical rows.
const laanSparSample = "\xef\xbb\xbf" +
	"28-02-2031;Netto Butik 123;-1.234,50;DKK;\r\n" +
	"27-02-2031;Overførsel til lån 1111222233;-2.500,00;DKK;\r\n" +
	"27-02-2031;Bager  Hansen;-45,00;DKK;\r\n" +
	"27-02-2031;Bager  Hansen;-45,00;DKK;\r\n" +
	"25-02-2031;Løn;31.000,00;DKK;\r\n"

func TestParseLaanSpar(t *testing.T) {
	f, _ := bankFormatByID("laanspar")
	rows, err := parseBankCSV([]byte(laanSparSample), f)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 5 {
		t.Fatalf("%d rows, want 5", len(rows))
	}
	r := rows[0]
	if r.Booked.Format("2006-01-02") != "2031-02-28" || r.Text != "Netto Butik 123" || r.Amount != -123450 || r.Seq != 1 || r.Line != 1 {
		t.Errorf("first row %+v", r)
	}
	if rows[2].Text != "Bager Hansen" || rows[2].Seq != 1 || rows[3].Seq != 2 {
		t.Errorf("identical rows %+v %+v, want seq 1 and 2 and spaces collapsed", rows[2], rows[3])
	}
	if rows[4].Amount != 3100000 {
		t.Errorf("income %d", rows[4].Amount)
	}
	if got := bankFormatsFor([]byte(laanSparSample)); len(got) != 1 || got[0].ID != "laanspar" {
		t.Errorf("formats for the sample: %v", got)
	}
}

func TestParseWindows1252(t *testing.T) {
	f, _ := bankFormatByID("laanspar")
	rows, err := parseBankCSV([]byte("01-03-2031;K\xf8b \x80 \x96 R\xe5vare;-10,00;DKK;\n"), f)
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].Text != "Køb € – Råvare" {
		t.Errorf("text %q", rows[0].Text)
	}
}

func TestParseTwoAmountColumns(t *testing.T) {
	f := bankFormat{Comma: ',', Header: true, Date: 0, Text: 1, Amount: -1, Debit: 2, Credit: 3,
		DateLayout: "2006-01-02", Decimal: ".", Thousands: ""}
	rows, err := parseBankCSV([]byte("Date,Text,Out,In\n2031-03-01,\"Rent, March\",5000.00,\n2031-03-02,Salary,,100.5\n"), f)
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].Text != "Rent, March" || rows[0].Amount != -500000 || rows[1].Amount != 10050 {
		t.Errorf("rows %+v", rows)
	}
}

func TestParseErrors(t *testing.T) {
	f, _ := bankFormatByID("laanspar")
	for _, tc := range []struct {
		data, key string
		line      int
	}{
		{"", "bank.err_empty", 0},
		{"01-03-2031;Ok;-1,00;DKK;\n2031-03-02;Bad date;-1,00;DKK;\n", "bank.err_date", 2},
		{"01-03-2031;Bad amount;1,2,3;DKK;\n", "bank.err_amount", 1},
		{"01-03-2031;Too few\n", "bank.err_columns", 1},
	} {
		_, err := parseBankCSV([]byte(tc.data), f)
		var pe *bankParseError
		if !errors.As(err, &pe) || pe.Key != tc.key || pe.Line != tc.line {
			t.Errorf("%q: error %v, want %s on line %d", tc.data, err, tc.key, tc.line)
		}
	}
	if got := bankFormatsFor([]byte("Date,Text,Amount\n")); len(got) != 0 {
		t.Errorf("formats for a foreign file: %v", got)
	}
}
