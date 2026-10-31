package main

import (
	"bytes"
	"encoding/csv"
	"errors"
	"io"
	"strings"
	"time"
	"unicode/utf8"
)

// bankFormat is the layout of one bank's CSV export. Supporting another bank
// is a new entry in bankFormats and a test with a made-up file in its layout.
type bankFormat struct {
	ID, Name           string
	Comma              rune
	Header             bool // the first row is column names
	Date, Text, Amount int  // column numbers, from 0
	Debit, Credit      int  // instead of Amount, for a bank with two columns; -1 if unused
	DateLayout         string
	Decimal, Thousands string
}

var bankFormats = []bankFormat{
	// Lån & Spar: date;text;amount;currency; with no header, newest first.
	{ID: "laanspar", Name: "Lån & Spar", Comma: ';', Date: 0, Text: 1, Amount: 2, Debit: -1, Credit: -1,
		DateLayout: "02-01-2006", Decimal: ",", Thousands: "."},
}

func bankFormatByID(id string) (bankFormat, bool) {
	for _, f := range bankFormats {
		if f.ID == id {
			return f, true
		}
	}
	return bankFormat{}, false
}

// bankRow is one transaction read from a file. Seq counts identical rows
// (same date, text and amount) in the file, 1 for the first, so two real
// purchases alike are both kept and a file imported again matches row for row.
type bankRow struct {
	Line   int
	Booked time.Time
	Text   string
	Amount Money
	Seq    int
}

func (r bankRow) key() string {
	return r.Booked.Format(time.DateOnly) + "\x00" + r.Text + "\x00" + r.Amount.format(".", "")
}

// bankParseError tells which line of the file could not be read. Key is a
// locale key, Value the offending text.
type bankParseError struct {
	Line  int
	Key   string
	Value string
}

func (e *bankParseError) Error() string { return e.Key + ": " + e.Value }

const maxBankText = 255 // the text column

// parseBankCSV reads a whole export. The file may be UTF-8, with or without a
// byte order mark, or else is read as Windows-1252, which older exports use.
func parseBankCSV(data []byte, f bankFormat) ([]bankRow, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	text := string(data)
	if !utf8.Valid(data) {
		text = decodeWindows1252(data)
	}
	r := csv.NewReader(strings.NewReader(text))
	r.Comma = f.Comma
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	need := max(f.Date, f.Text, f.Amount, f.Debit, f.Credit) + 1

	var rows []bankRow
	seen := map[string]int{}
	for first := true; ; first = false {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		line, _ := r.FieldPos(0)
		if err != nil {
			var pe *csv.ParseError
			if errors.As(err, &pe) {
				line = pe.Line
			}
			return nil, &bankParseError{Line: line, Key: "bank.err_csv", Value: err.Error()}
		}
		if first && f.Header {
			continue
		}
		if len(rec) < need {
			return nil, &bankParseError{Line: line, Key: "bank.err_columns", Value: strings.Join(rec, string(f.Comma))}
		}
		row := bankRow{Line: line}
		if row.Booked, err = time.Parse(f.DateLayout, strings.TrimSpace(rec[f.Date])); err != nil {
			return nil, &bankParseError{Line: line, Key: "bank.err_date", Value: rec[f.Date]}
		}
		if row.Amount, err = bankAmount(rec, f); err != nil {
			return nil, &bankParseError{Line: line, Key: "bank.err_amount", Value: err.Error()}
		}
		row.Text = strings.Join(strings.Fields(rec[f.Text]), " ")
		if utf8.RuneCountInString(row.Text) > maxBankText {
			row.Text = string([]rune(row.Text)[:maxBankText])
		}
		seen[row.key()]++
		row.Seq = seen[row.key()]
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return nil, &bankParseError{Key: "bank.err_empty"}
	}
	return rows, nil
}

// bankAmount is the row's amount, negative for money going out. With two
// columns the debit is taken as going out whatever its sign.
func bankAmount(rec []string, f bankFormat) (Money, error) {
	if f.Amount >= 0 {
		return parseMoney(rec[f.Amount], f.Decimal, f.Thousands)
	}
	debit, err := parseMoney(rec[f.Debit], f.Decimal, f.Thousands)
	if err != nil {
		return 0, err
	}
	credit, err := parseMoney(rec[f.Credit], f.Decimal, f.Thousands)
	if err != nil {
		return 0, err
	}
	return credit - max(debit, -debit), nil
}

// bankFormatsFor lists the formats that read data without errors.
func bankFormatsFor(data []byte) []bankFormat {
	var out []bankFormat
	for _, f := range bankFormats {
		if _, err := parseBankCSV(data, f); err == nil {
			out = append(out, f)
		}
	}
	return out
}

// windows1252 holds the characters of bytes 0x80–0x9F, where Windows-1252
// differs from Latin-1. The five unused bytes keep their Latin-1 meaning.
var windows1252 = []rune("€\u0081‚ƒ„…†‡ˆ‰Š‹Œ\u008dŽ\u008f\u0090‘’“”•–—˜™š›œ\u009džŸ")

func decodeWindows1252(b []byte) string {
	var s strings.Builder
	for _, c := range b {
		if c >= 0x80 && c <= 0x9f {
			s.WriteRune(windows1252[c-0x80])
		} else {
			s.WriteRune(rune(c))
		}
	}
	return s.String()
}
