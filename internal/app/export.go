package app

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"strings"
)

// budgetExport sends the budget as an OpenDocument spreadsheet laid out like
// the grid, with totals as formulas.
func (a *App) budgetExport(w http.ResponseWriter, r *http.Request) {
	year, ok := pathYear(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	current, _, ok, err := a.BudgetFor(r)
	if err != nil {
		a.Fail(w, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	b, err := a.loadBudget(r.Context(), current.ID, year)
	if err != nil {
		a.Fail(w, err)
		return
	}
	if b == nil {
		http.NotFound(w, r)
		return
	}
	var buf bytes.Buffer
	if err := writeODS(&buf, b, a.Locale(r)); err != nil {
		a.Fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.oasis.opendocument.spreadsheet")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="budget-%d.ods"`, year))
	buf.WriteTo(w)
}

// Spreadsheet columns: A section, B line name, C–N months, O year total.
func col(month int) string { return string(rune('B' + month)) } // 1 → C … 12 → N, 13 → O

type sheet struct {
	rows []string
	L    *Locale
}

func (s *sheet) add(cells ...string) int {
	if len(cells) == 0 {
		cells = []string{empty} // a row needs at least one cell
	}
	s.rows = append(s.rows, "<table:table-row>"+strings.Join(cells, "")+"</table:table-row>")
	return len(s.rows) // 1-based row number
}

func esc(s string) string {
	var b strings.Builder
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

func text(s, style string) string {
	return fmt.Sprintf(`<table:table-cell table:style-name="%s" office:value-type="string"><text:p>%s</text:p></table:table-cell>`, style, esc(s))
}

func number(m Money, style string) string {
	if m == 0 {
		return `<table:table-cell/>`
	}
	return fmt.Sprintf(`<table:table-cell table:style-name="%s" office:value-type="float" office:value="%s"/>`, style, m.Format(".", ""))
}

// formula stores the computed value too, so the numbers show without recalculation.
func formula(f string, m Money) string {
	return fmt.Sprintf(`<table:table-cell table:style-name="total" table:formula="of:=%s" office:value-type="float" office:value="%s"/>`, f, m.Format(".", ""))
}

const empty = `<table:table-cell/>`

// header writes a section heading with the month names.
func (s *sheet) header(title string) {
	cells := []string{text(title, "head"), empty}
	for _, m := range Months {
		cells = append(cells, text(s.L.T(fmt.Sprintf("month.%d", m)), "head"))
	}
	s.add(append(cells, text(s.L.T("budget.year"), "head"))...)
}

// section writes the lines and a total row and returns the total's row number.
func (s *sheet) section(key string, lines []*Line, total Totals) int {
	s.header(s.L.T("section." + key))
	first := len(s.rows) + 1
	for _, l := range lines {
		row := len(s.rows) + 1
		cells := []string{empty, text(l.Name, "")}
		for _, m := range Months {
			cells = append(cells, number(l.Amounts[m], "amount"))
		}
		s.add(append(cells, formula(fmt.Sprintf("SUM([.C%d:.N%d])", row, row), l.Amounts[0]))...)
	}
	last := len(s.rows)
	row := last + 1
	cells := []string{empty, text(s.L.T("calc."+key), "head")}
	for _, m := range Months {
		if last >= first {
			cells = append(cells, formula(fmt.Sprintf("SUM([.%s%d:.%s%d])", col(m), first, col(m), last), total[m]))
		} else {
			cells = append(cells, formula("0", 0))
		}
	}
	return s.add(append(cells, formula(fmt.Sprintf("SUM([.C%d:.N%d])", row, row), total[0]))...)
}

// calc writes a row where every month (and the year) is expr applied to that column.
func (s *sheet) calc(label string, expr func(c string) string, values Totals) int {
	cells := []string{empty, text(label, "head")}
	for _, m := range Months {
		cells = append(cells, formula(expr(col(m)), values[m]))
	}
	return s.add(append(cells, formula(expr("O"), values[0]))...)
}

func writeODS(w io.Writer, b *BudgetYear, L *Locale) error {
	income, expense, allocation := b.total("income"), b.total("expense"), b.total("allocation")
	var net, diff Totals
	for i := range net {
		net[i] = income[i] - expense[i]
		diff[i] = net[i] - allocation[i]
	}

	s := &sheet{L: L}
	s.add(empty, text(L.T("budget.title", b.Year), "title"))
	s.add()
	incRow := s.section("income", b.Lines["income"], income)
	s.add()
	expRow := s.section("expense", b.Lines["expense"], expense)
	s.add()
	s.header(L.T("section.disposable"))
	ref := func(row int) func(string) string {
		return func(c string) string { return fmt.Sprintf("[.%s%d]", c, row) }
	}
	inc2 := s.calc(L.T("calc.income"), ref(incRow), income)
	exp2 := s.calc(L.T("calc.expense"), ref(expRow), expense)
	netRow := s.calc(L.T("calc.net"), func(c string) string { return fmt.Sprintf("[.%s%d]-[.%s%d]", c, inc2, c, exp2) }, net)
	s.add()
	allocRow := s.section("allocation", b.Lines["allocation"], allocation)
	s.calc(L.T("calc.difference"), func(c string) string { return fmt.Sprintf("[.%s%d]-[.%s%d]", c, netRow, c, allocRow) }, diff)

	z := zip.NewWriter(w)
	// The mimetype must come first, uncompressed and without a data
	// descriptor, or LibreOffice refuses the file; CreateRaw allows that.
	mimetype := []byte("application/vnd.oasis.opendocument.spreadsheet")
	mt, err := z.CreateRaw(&zip.FileHeader{
		Name:               "mimetype",
		Method:             zip.Store,
		CRC32:              crc32.ChecksumIEEE(mimetype),
		CompressedSize64:   uint64(len(mimetype)),
		UncompressedSize64: uint64(len(mimetype)),
	})
	if err != nil {
		return err
	}
	if _, err := mt.Write(mimetype); err != nil {
		return err
	}
	for _, f := range []struct{ name, body string }{
		{"META-INF/manifest.xml", odsManifest},
		{"content.xml", fmt.Sprintf(odsContent, esc(L.T("budget.title", b.Year)), strings.Join(s.rows, "\n"))},
	} {
		fw, err := z.Create(f.name)
		if err != nil {
			return err
		}
		if _, err := io.WriteString(fw, f.body); err != nil {
			return err
		}
	}
	return z.Close()
}

const odsManifest = `<?xml version="1.0" encoding="UTF-8"?>
<manifest:manifest xmlns:manifest="urn:oasis:names:tc:opendocument:xmlns:manifest:1.0" manifest:version="1.2">
 <manifest:file-entry manifest:full-path="/" manifest:version="1.2" manifest:media-type="application/vnd.oasis.opendocument.spreadsheet"/>
 <manifest:file-entry manifest:full-path="content.xml" manifest:media-type="text/xml"/>
</manifest:manifest>
`

const odsContent = `<?xml version="1.0" encoding="UTF-8"?>
<office:document-content
 xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0"
 xmlns:style="urn:oasis:names:tc:opendocument:xmlns:style:1.0"
 xmlns:text="urn:oasis:names:tc:opendocument:xmlns:text:1.0"
 xmlns:table="urn:oasis:names:tc:opendocument:xmlns:table:1.0"
 xmlns:fo="urn:oasis:names:tc:opendocument:xmlns:xsl-fo-compatible:1.0"
 xmlns:number="urn:oasis:names:tc:opendocument:xmlns:datastyle:1.0"
 xmlns:of="urn:oasis:names:tc:opendocument:xmlns:of:1.2"
 office:version="1.2">
<office:automatic-styles>
 <number:number-style style:name="N2"><number:number number:decimal-places="2" number:min-integer-digits="1" number:grouping="true"/></number:number-style>
 <style:style style:name="amount" style:family="table-cell" style:data-style-name="N2"/>
 <style:style style:name="total" style:family="table-cell" style:data-style-name="N2"><style:text-properties fo:font-weight="bold"/></style:style>
 <style:style style:name="head" style:family="table-cell"><style:text-properties fo:font-weight="bold"/></style:style>
 <style:style style:name="title" style:family="table-cell"><style:text-properties fo:font-weight="bold" fo:font-size="14pt"/></style:style>
 <style:style style:name="colA" style:family="table-column"><style:table-column-properties style:column-width="3cm"/></style:style>
 <style:style style:name="colB" style:family="table-column"><style:table-column-properties style:column-width="6.5cm"/></style:style>
 <style:style style:name="colN" style:family="table-column"><style:table-column-properties style:column-width="2.4cm"/></style:style>
</office:automatic-styles>
<office:body><office:spreadsheet>
<table:table table:name="%s">
<table:table-column table:style-name="colA"/>
<table:table-column table:style-name="colB"/>
<table:table-column table:style-name="colN" table:number-columns-repeated="13"/>
%s
</table:table>
</office:spreadsheet></office:body>
</office:document-content>
`
