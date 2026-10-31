package app

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"io"
	"strconv"
	"strings"
	"testing"
)

func testBudget() *BudgetYear {
	line := func(id int64, section, name string, amounts map[int]Money) *Line {
		l := &Line{ID: id, Section: section, Name: name}
		for m, v := range amounts {
			l.Amounts[m] = v
			l.Amounts[0] += v
		}
		return l
	}
	return &BudgetYear{Year: 2030, Lines: map[string][]*Line{
		"income": {
			line(1, "income", "Salary A", map[int]Money{1: 3000000, 2: 3000000, 5: 3100050}),
			line(2, "income", "Salary B <&>", map[int]Money{1: 2500000, 5: 2500000}),
		},
		"expense": {
			line(3, "expense", "Rent", map[int]Money{1: 1200000, 2: 1200000, 5: 1200000}),
			line(4, "expense", "Insurance", map[int]Money{4: 450075}),
		},
		"allocation": {
			line(5, "allocation", "Savings", map[int]Money{1: 2000000, 5: 1000000}),
		},
	}}
}

// cell is one spreadsheet cell as written: its value and formula, if any.
type cell struct {
	value   float64
	formula string
}

// readSheet unpacks the export and returns its cells keyed like "C5".
func readSheet(t *testing.T, data []byte) map[string]cell {
	t.Helper()
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	// LibreOffice refuses the file unless mimetype comes first, stored, and
	// without a data descriptor (flag bit 3).
	if f := z.File[0]; f.Name != "mimetype" || f.Method != zip.Store || f.Flags&0x8 != 0 {
		t.Fatalf("first entry is %q (method %d, flags %#x), want stored mimetype without data descriptor", f.Name, f.Method, f.Flags)
	}
	var content []byte
	for _, f := range z.File {
		if f.Name == "content.xml" {
			rc, _ := f.Open()
			content, _ = io.ReadAll(rc)
		}
	}
	cells := map[string]cell{}
	dec := xml.NewDecoder(bytes.NewReader(content))
	row, colIdx := 0, 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("content.xml is not valid XML: %v", err)
		}
		el, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch el.Name.Local {
		case "table-row":
			row++
			colIdx = 0
		case "table-cell":
			var c cell
			for _, a := range el.Attr {
				switch a.Name.Local {
				case "value":
					c.value, _ = strconv.ParseFloat(a.Value, 64)
				case "formula":
					c.formula = a.Value
				}
			}
			cells[string(rune('A'+colIdx))+strconv.Itoa(row)] = c
			colIdx++
		}
	}
	return cells
}

// eval computes a cell from the formulas the export uses: SUM over a range,
// a reference, a difference of two references and constants.
func eval(cells map[string]cell, ref string) float64 {
	c := cells[ref]
	f := strings.TrimPrefix(c.formula, "of:=")
	if f == "" {
		return c.value
	}
	unref := func(s string) string { return strings.Trim(s, "[].") }
	if strings.HasPrefix(f, "SUM(") {
		from, to, _ := strings.Cut(strings.TrimSuffix(strings.TrimPrefix(f, "SUM("), ")"), ":")
		from, to = unref(from), unref(to)
		r1, _ := strconv.Atoi(from[1:])
		r2, _ := strconv.Atoi(to[1:])
		sum := 0.0
		for col := from[0]; col <= to[0]; col++ {
			for row := r1; row <= r2; row++ {
				sum += eval(cells, string(col)+strconv.Itoa(row))
			}
		}
		return sum
	}
	if a, b, ok := strings.Cut(f, "-"); ok {
		return eval(cells, unref(a)) - eval(cells, unref(b))
	}
	if strings.HasPrefix(f, "[") {
		return eval(cells, unref(f))
	}
	v, _ := strconv.ParseFloat(f, 64)
	return v
}

func TestExportODS(t *testing.T) {
	en := &Locale{Code: "en", Decimal: ".", Thousands: ",", msgs: map[string]string{"budget.title": "Budget %d"}}
	var buf bytes.Buffer
	if err := writeODS(&buf, testBudget(), en); err != nil {
		t.Fatal(err)
	}
	cells := readSheet(t, buf.Bytes())

	formulas := 0
	for ref, c := range cells {
		if c.formula == "" {
			continue
		}
		formulas++
		if got := eval(cells, ref); got != c.value {
			t.Errorf("%s: formula %s gives %.2f but the stored value is %.2f", ref, c.formula, got, c.value)
		}
	}
	if formulas == 0 {
		t.Fatal("no formulas in the export")
	}

	// The last row is Difference; its year total must match the budget's own maths.
	b := testBudget()
	in, ex, al := b.total("income"), b.total("expense"), b.total("allocation")
	want := float64(in[0]-ex[0]-al[0]) / 100
	last := 0
	for ref := range cells {
		if n, _ := strconv.Atoi(ref[1:]); n > last {
			last = n
		}
	}
	if got := eval(cells, "O"+strconv.Itoa(last)); got != want {
		t.Errorf("difference for the year = %.2f, want %.2f", got, want)
	}
}
