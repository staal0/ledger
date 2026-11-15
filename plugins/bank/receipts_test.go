package bank

import (
	"archive/zip"
	"bytes"
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
	app.MustStatus(t, app.TestRequest(a, "POST", "/bank/accounts", url.Values{"name": {"Budget account"}, "format": {"laanspar"}}, ""), 303)
	app.MustStatus(t, app.TestRequest(a, "POST", "/bank/accounts", url.Values{"name": {"Receipts"}, "format": {"nexi"}}, ""), 303)
	bank := strconv.FormatInt(count("SELECT id FROM bank_accounts WHERE format = 'laanspar'"), 10)
	receipts := strconv.FormatInt(count("SELECT id FROM bank_accounts WHERE format = 'nexi'"), 10)
	app.MustStatus(t, app.TestRequest(a, "POST", "/bank/accounts/"+receipts,
		url.Values{"name": {"Receipts"}, "format": {"nexi"}, "default": {"allocation:Food account"}}, ""), 303)
	app.MustStatus(t, app.TestRequest(a, "POST", "/bank/rules", url.Values{"new_pattern": {"soap"}, "new_target": {"expense:Household"}}, ""), 303)
	previewAndImport(t, a, bank, "07-03-2031;Card Grocer;-120,00;DKK;\r\n20-03-2031;Card Kiosk;-15,50;DKK;\r\n")

	w := uploadBank(t, a, receipts, zipped(t, "storebox/receipts.json", receiptsSample))
	app.MustStatus(t, w, 303)
	if loc := w.Header().Get("Location"); !strings.Contains(loc, "added=2") {
		t.Errorf("import went to %s, want 2 added", loc)
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

	// The export grows: imported again, nothing is added and the split stays.
	w = uploadBank(t, a, receipts, receiptsSample)
	app.MustStatus(t, w, 303)
	if loc := w.Header().Get("Location"); !strings.Contains(loc, "added=0") || !strings.Contains(loc, "skipped=2") {
		t.Errorf("second import went to %s", loc)
	}
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

	if w := uploadBank(t, a, bank, receiptsSample); w.Code != 400 || !strings.Contains(w.Body.String(), "receipt export") {
		t.Errorf("a receipt export to a bank account: status %d, %s", w.Code, w.Body.String())
	}
	if w := uploadBank(t, a, receipts, "not json"); w.Code != 400 {
		t.Errorf("a file that is no export: status %d, want 400", w.Code)
	}
}
