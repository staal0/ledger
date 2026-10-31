package bank

import (
	"bytes"
	"html"
	"mime/multipart"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/staal0/ledger/internal/app"
)

// uploadBank posts a file to the preview as the browser's form does.
func uploadBank(t *testing.T, a *app.App, account, data string) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	m := multipart.NewWriter(&body)
	if err := m.WriteField("account", account); err != nil {
		t.Fatal(err)
	}
	f, err := m.CreateFormFile("file", "export.csv")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte(data)); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/bank/preview", &body)
	r.Header.Set("Content-Type", m.FormDataContentType())
	r.Header.Set("Accept-Language", "en")
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	return w
}

// previewAndImport reads data for account and imports what the preview offers.
func previewAndImport(t *testing.T, a *app.App, account, data string) string {
	t.Helper()
	w := uploadBank(t, a, account, data)
	app.MustStatus(t, w, 200)
	page := html.UnescapeString(w.Body.String())
	m := regexp.MustCompile(`name="data" value="([^"]*)"`).FindStringSubmatch(w.Body.String())
	if m == nil {
		t.Fatalf("preview has no import form: %s", page)
	}
	app.MustStatus(t, app.TestRequest(a, "POST", "/bank/import", url.Values{"account": {account}, "data": {html.UnescapeString(m[1])}}, ""), 303)
	return page
}

func TestBankImport(t *testing.T) {
	a := app.TestApp(t, false)
	app.TestExec(t, "INSERT INTO settings (name, value) VALUES ('plugin.bank', '1')")
	app.MustStatus(t, app.TestRequest(a, "POST", "/bank/accounts", url.Values{"name": {"Budget account"}, "format": {"laanspar"}}, ""), 303)
	app.MustStatus(t, app.TestRequest(a, "POST", "/bank/accounts", url.Values{"name": {"x"}, "format": {"nope"}}, ""), 400)
	account := strconv.FormatInt(app.TestQueryInt(t, "SELECT id FROM bank_accounts WHERE name = 'Budget account'"), 10)

	page := previewAndImport(t, a, account, laanSparSample)
	if !strings.Contains(page, "5 new transactions, 0 imported before.") {
		t.Errorf("first preview: %s", page)
	}
	if n := app.TestQueryInt(t, "SELECT COUNT(*) FROM bank_transactions"); n != 5 {
		t.Fatalf("%d transactions after the first import, want 5", n)
	}
	if n := app.TestQueryInt(t, "SELECT CAST(SUM(amount) * 100 AS SIGNED) FROM bank_transactions WHERE text = 'Bager Hansen'"); n != -9000 {
		t.Errorf("the two identical rows sum to %d, want both kept", n)
	}

	// The same file again adds nothing.
	w := uploadBank(t, a, account, laanSparSample)
	if page := html.UnescapeString(w.Body.String()); !strings.Contains(page, "0 new transactions, 5 imported before.") {
		t.Errorf("second preview: %s", page)
	}

	// An overlapping export: the identical row now three times, and one new day.
	later := "01-03-2031;Netto Butik 123;-99,95;DKK;\r\n" +
		"27-02-2031;Bager Hansen;-45,00;DKK;\r\n" +
		"27-02-2031;Bager Hansen;-45,00;DKK;\r\n" +
		"27-02-2031;Bager Hansen;-45,00;DKK;\r\n"
	if page := previewAndImport(t, a, account, later); !strings.Contains(page, "2 new transactions, 2 imported before.") {
		t.Errorf("overlapping preview: %s", page)
	}
	if n := app.TestQueryInt(t, "SELECT COUNT(*) FROM bank_transactions"); n != 7 {
		t.Fatalf("%d transactions after the overlapping import, want 7", n)
	}
	page = html.UnescapeString(app.TestRequest(a, "GET", "/bank/transactions?show=all", nil, "").Body.String())
	for _, want := range []string{"<title>CSV import", "Overførsel til lån 1111222233", "-1,234.50", "31,000"} {
		if !strings.Contains(page, want) {
			t.Errorf("bank page lacks %q", want)
		}
	}

	// A second account's transactions are listed apart when it is chosen.
	app.MustStatus(t, app.TestRequest(a, "POST", "/bank/accounts", url.Values{"name": {"Savings"}, "format": {"laanspar"}}, ""), 303)
	savings := strconv.FormatInt(app.TestQueryInt(t, "SELECT id FROM bank_accounts WHERE name = 'Savings'"), 10)
	previewAndImport(t, a, savings, "15-06-2030;Renter;12,34;DKK;\r\n")
	page = html.UnescapeString(app.TestRequest(a, "GET", "/bank/transactions?show=all&account="+savings+"&year=2031", nil, "").Body.String())
	if !strings.Contains(page, "Renter") || strings.Contains(page, "Bager Hansen") {
		t.Errorf("the savings account shows other transactions, or not its own in its only year")
	}
	page = html.UnescapeString(app.TestRequest(a, "GET", "/bank/transactions?show=all&account="+account, nil, "").Body.String())
	if strings.Contains(page, "Renter") || !strings.Contains(page, "Bager Hansen") {
		t.Errorf("the budget account shows the savings account's transactions")
	}
	app.MustStatus(t, app.TestRequest(a, "POST", "/bank/accounts/"+savings+"/delete", url.Values{}, ""), 303)

	// Undoing the last import removes only its rows.
	last := strconv.FormatInt(app.TestQueryInt(t, "SELECT MAX(id) FROM bank_imports"), 10)
	app.MustStatus(t, app.TestRequest(a, "POST", "/bank/imports/"+last+"/delete", url.Values{}, ""), 303)
	if n := app.TestQueryInt(t, "SELECT COUNT(*) FROM bank_transactions"); n != 5 {
		t.Errorf("%d transactions after undo, want 5", n)
	}

	// One transaction can be deleted; importing a file with it brings it back.
	salary := strconv.FormatInt(app.TestQueryInt(t, "SELECT id FROM bank_transactions WHERE text = 'Løn'"), 10)
	app.MustStatus(t, app.TestRequest(a, "POST", "/bank/transactions", url.Values{"action": {"delete:" + salary}, "year": {"2031"}}, ""), 303)
	if n := app.TestQueryInt(t, "SELECT COUNT(*) FROM bank_transactions"); n != 4 {
		t.Errorf("%d transactions after deleting one, want 4", n)
	}
	if page := previewAndImport(t, a, account, laanSparSample); !strings.Contains(page, "1 new transactions, 4 imported before.") {
		t.Errorf("preview after a delete: %s", page)
	}
	if n := app.TestQueryInt(t, "SELECT COUNT(*) FROM bank_transactions WHERE text = 'Løn'"); n != 1 {
		t.Errorf("the deleted transaction is back %d times, want once", n)
	}
	app.MustStatus(t, app.TestRequest(a, "POST", "/bank/transactions", url.Values{"action": {"delete:999999"}}, ""), 303)
	if n := app.TestQueryInt(t, "SELECT COUNT(*) FROM bank_transactions"); n != 5 {
		t.Errorf("deleting an unknown transaction changed the count to %d", n)
	}

	// A file in another layout is not imported, and the line is named.
	w = uploadBank(t, a, account, "01-03-2031;Ok;-1,00;DKK;\n2031-03-02;Bad;-1,00;DKK;\n")
	app.MustStatus(t, w, 200)
	if page := html.UnescapeString(w.Body.String()); !strings.Contains(page, "Line 2:") || strings.Contains(page, `name="data"`) {
		t.Errorf("bad file preview: %s", page)
	}

	app.MustStatus(t, app.TestRequest(a, "POST", "/bank/accounts/"+account+"/delete", url.Values{}, ""), 303)
	if n := app.TestQueryInt(t, "SELECT COUNT(*) FROM bank_transactions"); n != 0 {
		t.Errorf("%d transactions left after deleting the account", n)
	}
	app.MustStatus(t, app.TestRequest(a, "POST", "/bank/import", url.Values{"account": {account}, "data": {""}}, ""), 404)
}
