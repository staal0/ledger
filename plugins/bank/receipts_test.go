package bank

import (
	"archive/zip"
	"bytes"
	"html"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/staal0/ledger/internal/app"
)

// receiptsSample is a made-up receipt export: one receipt the bank has too, one paid otherwise.
const receiptsSample = `[
 {"receiptId": "r1", "purchaseDateTimeString": "2031-03-05T17:30:00+01:00", "merchant": {"name": "Grocer"},
  "price": {"amount": 120.0}, "receiptImage": {"bytes": "AAAA"},
  "receiptLines": [
   {"name": "MILK 1L", "totalPrice": {"amount": 20.0}},
   {"name": "DISH SOAP", "totalPrice": {"amount": 30.0}},
   {"name": "BREAD", "totalPrice": {"amount": 70.0}}]},
 {"receiptId": "r2", "purchaseDateTimeString": "2031-03-06T09:00:00+01:00", "merchant": {"name": "Grocer"},
  "price": {"amount": 15.5}, "receiptLines": [{"name": "MILK 1L", "totalPrice": {"amount": 15.5}}]}
]`

func zipped(t *testing.T, name, data string) string {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	f, err := z.Create(name)
	if err == nil {
		_, err = f.Write([]byte(data))
	}
	if err == nil {
		err = z.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestBankReceipts(t *testing.T) {
	a := app.TestApp(t, false)
	app.TestExec(t, "INSERT INTO settings (name, value) VALUES ('plugin.bank', '1')")
	lines := app.TestSetupYear(t, a, 2031, "allocation/Food account", "expense/Household")
	count := func(q string, args ...any) int64 { return app.TestQueryInt(t, q, args...) }
	app.MustStatus(t, app.TestRequest(a, "POST", "/bank/accounts", url.Values{"name": {"Budget account"}}, ""), 303)
	app.MustStatus(t, app.TestRequest(a, "POST", "/bank/accounts", url.Values{"name": {"Receipts"}}, ""), 303)
	bank := strconv.FormatInt(count("SELECT id FROM bank_accounts WHERE name = 'Budget account'"), 10)
	receipts := strconv.FormatInt(count("SELECT id FROM bank_accounts WHERE name = 'Receipts'"), 10)
	app.MustStatus(t, app.TestRequest(a, "POST", "/bank/accounts/"+receipts,
		url.Values{"name": {"Receipts"}, "default": {"allocation:Food account"}}, ""), 303)
	app.MustStatus(t, app.TestRequest(a, "POST", "/bank/rules", url.Values{"new_pattern": {"soap"}, "new_target": {"expense:Household"}}, ""), 303)
	previewAndImport(t, a, bank, "07-03-2031;Card Grocer;-120,00;DKK;\r\n20-03-2031;Card Kiosk;-15,50;DKK;\r\n")

	// The preview shows the new receipts and the bank row the first covers.
	page := previewAndImport(t, a, receipts, zipped(t, "storebox/receipts.json", receiptsSample))
	for _, want := range []string{"2 new receipts, 0 imported before", "covers 2031-03-07 Budget account: Card Grocer"} {
		if !strings.Contains(page, want) {
			t.Errorf("preview lacks %q", want)
		}
	}
	if n := count("SELECT COUNT(*) FROM bank_receipt_items"); n != 4 {
		t.Errorf("%d receipt items imported, want 4", n)
	}

	// The bank's row for the first receipt is ignored; the receipt is split by its items.
	if n := count("SELECT COUNT(*) FROM bank_transactions WHERE text = 'Card Grocer' AND ignored AND by_receipt"); n != 1 {
		t.Error("the bank row a receipt covers is not ignored")
	}
	r1 := count("SELECT id FROM bank_transactions WHERE receipt = 'r1'")
	if got := app.TestQueryInts(t, "SELECT CAST(-amount * 100 AS SIGNED) FROM bank_splits WHERE transaction_id = ? ORDER BY line_id = ?", r1, lines["expense/Household"]); len(got) != 2 || got[0] != 9000 || got[1] != 3000 {
		t.Errorf("receipt split %v, want food 90 and household 30", got)
	}
	// The second has no item rule: whole, on the account's default line; the kiosk row is two weeks later.
	if n := count("SELECT COUNT(*) FROM bank_transactions WHERE receipt = 'r2' AND by_default AND NOT split AND line_id = ?", lines["allocation/Food account"]); n != 1 {
		t.Error("the receipt without item rules is not on the default line")
	}
	if n := count("SELECT COUNT(*) FROM bank_transactions WHERE text = 'Card Kiosk' AND NOT ignored"); n != 1 {
		t.Error("a bank row two weeks after a receipt of the same amount was taken")
	}

	// A line's stats page shows the receipts on it: each item on the line its receipt was split to.
	stats := func(section, name string) string {
		w := app.TestRequest(a, "GET", "/stats/line?section="+section+"&name="+url.QueryEscape(name)+"&from=2031&to=2031", nil, "")
		app.MustStatus(t, w, 200)
		return html.UnescapeString(w.Body.String())
	}
	food, household := stats("allocation", "Food account"), stats("expense", "Household")
	for _, want := range []string{`"Actual":[null,null,10550,null`, `data-actual="Receipts"`, "Receipts by store", "Grocer", "BREAD", "MILK 1L"} {
		if !strings.Contains(food, want) {
			t.Errorf("the food line's stats lack %q", want)
		}
	}
	if strings.Contains(food, "DISH SOAP") || !strings.Contains(household, "DISH SOAP") {
		t.Error("the soap is not on the household line's stats alone")
	}

	// A month of the chart opens what is on the line then, receipts with their items.
	if !strings.Contains(food, `/bank/line-month?month=3`) {
		t.Error("the chart's months do not link to their page")
	}
	w := app.TestRequest(a, "GET", "/bank/line-month?section=allocation&name=Food+account&year=2031&month=3&from=2029&to=2031", nil, "")
	app.MustStatus(t, w, 200)
	month := html.UnescapeString(w.Body.String())
	for _, want := range []string{"105.50 in all, from 2 purchases, 2 of them with a receipt", "BREAD", "→ Household", "of 120<", "Next month", "/stats/line?from=2029&name=Food+account&section=allocation&to=2031", "month=4&name=Food+account&section=allocation&year=2031&from=2029&to=2031"} {
		if !strings.Contains(month, want) {
			t.Errorf("the month page lacks %q", want)
		}
	}
	app.MustStatus(t, app.TestRequest(a, "GET", "/bank/line-month?section=allocation&name=Food+account&year=2031&month=13", nil, ""), 404)

	// The export grows: imported again, nothing is added and the split stays.
	w = uploadBank(t, a, receipts, receiptsSample)
	app.MustStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), "0 new receipts, 2 imported before") {
		t.Error("the preview of the same export again does not show both as imported before")
	}
	app.MustStatus(t, app.TestRequest(a, "POST", "/bank/import", url.Values{"account": {receipts}, "receipts": {"[{}]"}}, ""), 400)
	if n := count("SELECT COUNT(*) FROM bank_splits"); n != 2 {
		t.Errorf("%d split parts after importing again, want 2", n)
	}

	// The receipt's items are shown with its split.
	w = app.TestRequest(a, "GET", "/bank/transactions/"+strconv.FormatInt(r1, 10)+"/split", nil, "")
	app.MustStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), "DISH SOAP") {
		t.Error("the split page does not show the receipt's items")
	}

	// Without the item rule the receipt is whole again, on its default line.
	app.MustStatus(t, app.TestRequest(a, "POST", "/bank/rules", url.Values{"action": {"delete:" + strconv.FormatInt(count("SELECT id FROM bank_rules"), 10)}}, ""), 303)
	if n := count("SELECT COUNT(*) FROM bank_transactions WHERE id = ? AND NOT split AND line_id = ?", r1, lines["allocation/Food account"]); n != 1 {
		t.Error("the receipt did not follow its item rule going")
	}

	// Neither receipts nor a bank file: the preview says why.
	if w := uploadBank(t, a, receipts, "not a file we know"); w.Code != 200 || !strings.Contains(w.Body.String(), `class="notice"`) {
		t.Errorf("an unknown file: status %d", w.Code)
	}
}
