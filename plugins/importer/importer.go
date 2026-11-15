// Package importer is the import plugin: it imports transactions from the banks'
// CSV exports into a budget's bank accounts, assigns them to budget lines by
// hand and by rules, compares them with the budget and fills the budget in.
//
// A file is read twice: once for a preview that marks the rows already
// imported, and again, sent back with the preview's form, when the import is
// confirmed. It is never stored.
package importer

import (
	"context"
	"database/sql"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/staal0/ledger/internal/app"
)

//go:embed locales migrations templates
var files embed.FS

type importPlugin struct{}

func init() { app.Register(importPlugin{}) }

// handlers serves the plugin's pages with the app's helpers.
type handlers struct{ *app.App }

func (importPlugin) ID() string { return "import" }

func sub(dir string) fs.FS {
	f, _ := fs.Sub(files, dir)
	return f
}

func (importPlugin) Migrations() fs.FS { return sub("migrations") }
func (importPlugin) Templates() fs.FS  { return sub("templates") }
func (importPlugin) Locales() fs.FS    { return sub("locales") }

func (importPlugin) Routes(mux *http.ServeMux, core *app.App) {
	a := handlers{core}
	mux.HandleFunc("GET /import", a.importPage)
	mux.HandleFunc("POST /import/accounts", a.Only(app.RoleEditor, a.accountCreate))
	mux.HandleFunc("POST /import/accounts/{id}", a.Only(app.RoleEditor, a.accountUpdate))
	mux.HandleFunc("POST /import/accounts/{id}/delete", a.Only(app.RoleEditor, a.accountDelete))
	mux.HandleFunc("POST /import/preview", a.Only(app.RoleEditor, a.previewPage))
	mux.HandleFunc("POST /import/import", a.Only(app.RoleEditor, a.importConfirm))
	mux.HandleFunc("POST /import/imports/{id}/delete", a.Only(app.RoleEditor, a.importDelete))
	mux.HandleFunc("GET /import/transactions", a.transactionsPage)
	mux.HandleFunc("POST /import/transactions", a.Only(app.RoleEditor, a.transactionsSave))
	mux.HandleFunc("GET /import/transactions/{id}/split", a.Only(app.RoleEditor, a.splitPage))
	mux.HandleFunc("POST /import/transactions/{id}/split", a.Only(app.RoleEditor, a.splitSave))
	mux.HandleFunc("GET /import/rules", a.rulesPage)
	mux.HandleFunc("POST /import/rules", a.Only(app.RoleEditor, a.rulesSave))
	mux.HandleFunc("GET /import/compare", a.comparePage)
	mux.HandleFunc("GET /import/line-month", a.lineMonthPage)
	mux.HandleFunc("GET /import/fill", a.Only(app.RoleEditor, a.fillPage))
	mux.HandleFunc("POST /import/fill", a.Only(app.RoleEditor, a.fillSave))
}

func (importPlugin) Nav() []app.NavItem { return []app.NavItem{{Path: "/import", Label: "nav.import"}} }

const maxBankFile = 2 << 20 // bytes; an export of a year is far smaller

type account struct {
	ID           int64
	BudgetID     int64
	Name         string
	Format       string // the bank format its last file had
	Transactions int
	Default      string // the default line as "section:name", or ""
}

// importURL is the plugin's page at path for budget b, with query q.
func importURL(path string, b app.BudgetRef, q url.Values) string {
	if q == nil {
		q = url.Values{}
	}
	if !b.IsDefault {
		q.Set("budget", b.Slug)
	}
	if len(q) == 0 {
		return path
	}
	return path + "?" + q.Encode()
}

type importTab struct {
	URL, Label string
	Active     bool
}

// importTabs are the plugin's pages for budget b, with active marked.
func importTabs(L *app.Locale, b app.BudgetRef, active string) []importTab {
	var tabs []importTab
	for _, t := range []struct{ key, path string }{{"import", "/import"}, {"transactions", "/import/transactions"}, {"rules", "/import/rules"}, {"compare", "/import/compare"}} {
		tabs = append(tabs, importTab{importURL(t.path, b, nil), L.T("import.tab_" + t.key), t.key == active})
	}
	return tabs
}

func (a handlers) listAccounts(ctx context.Context, budget int64) ([]account, error) {
	rows, err := a.DB().QueryContext(ctx, `SELECT a.id, a.budget_id, a.name, a.format,
			(SELECT COUNT(*) FROM import_transactions t WHERE t.account_id = a.id),
			COALESCE(CONCAT(a.default_section, ':', a.default_line_name), '')
		FROM import_accounts a WHERE a.budget_id = ? ORDER BY a.name`, budget)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []account
	for rows.Next() {
		var b account
		if err := rows.Scan(&b.ID, &b.BudgetID, &b.Name, &b.Format, &b.Transactions, &b.Default); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// accountFor is the account with the given id, and its budget, when the
// user may see that budget; ok is false otherwise.
func (a handlers) accountFor(r *http.Request, id string) (acc account, b app.BudgetRef, ok bool, err error) {
	err = a.DB().QueryRowContext(r.Context(), `SELECT id, budget_id, name, format FROM import_accounts WHERE id = ?`, id).
		Scan(&acc.ID, &acc.BudgetID, &acc.Name, &acc.Format)
	if errors.Is(err, sql.ErrNoRows) {
		return acc, b, false, nil
	}
	if err != nil {
		return acc, b, false, err
	}
	visible, err := a.VisibleBudgets(r.Context(), app.CurrentUser(r))
	for _, v := range visible {
		if v.ID == acc.BudgetID {
			return acc, v, true, err
		}
	}
	return acc, b, false, err
}

// accountForm reads and checks an account's name.
func accountForm(r *http.Request) (name string, ok bool) {
	name = strings.TrimSpace(r.FormValue("name"))
	return name, name != "" && utf8.RuneCountInString(name) <= 100
}

type importRow struct {
	ID         int64
	Account    string
	User       string
	ImportedAt time.Time
	Added      int
	Skipped    int
	Left       int // of the added, those not deleted since
}

// importPage lists the chosen budget's accounts and imports, with the form to import a file.
func (a handlers) importPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	current, visible, ok, err := a.ChosenBudget(r)
	if err != nil {
		a.Fail(w, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	accounts, err := a.listAccounts(ctx, current.ID)
	if err != nil {
		a.Fail(w, err)
		return
	}

	var total int
	if err := a.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM import_imports i JOIN import_accounts a ON a.id = i.account_id
		WHERE a.budget_id = ?`, current.ID).Scan(&total); err != nil {
		a.Fail(w, err)
		return
	}
	pages := app.NewPager(r, a.Locale(r), "page", total, "#imports")
	var imports []importRow
	rows, err := a.DB().QueryContext(ctx, `SELECT i.id, a.name, COALESCE(u.name, ''), i.imported_at, i.added, i.skipped,
			(SELECT COUNT(*) FROM import_transactions t WHERE t.import_id = i.id)
		FROM import_imports i JOIN import_accounts a ON a.id = i.account_id LEFT JOIN users u ON u.id = i.user_id
		WHERE a.budget_id = ? ORDER BY i.id DESC LIMIT ? OFFSET ?`, current.ID, pages.Size, pages.Offset())
	if err != nil {
		a.Fail(w, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var i importRow
		if err := rows.Scan(&i.ID, &i.Account, &i.User, &i.ImportedAt, &i.Added, &i.Skipped, &i.Left); err != nil {
			a.Fail(w, err)
			return
		}
		imports = append(imports, i)
	}
	if err := rows.Err(); err != nil {
		a.Fail(w, err)
		return
	}

	names, err := lineNames(ctx, a.DB(), current.ID)
	if err != nil {
		a.Fail(w, err)
		return
	}
	added, _ := strconv.Atoi(r.FormValue("added"))
	L := a.Locale(r)
	a.Render(w, r, "import", map[string]any{
		"Title":    L.T("nav.import"),
		"Tabs":     importTabs(L, current, "import"),
		"Current":  current,
		"Budgets":  app.Pickable(visible),
		"Accounts": accounts,
		"Sections": groupLines(names),
		"Imports":  imports,
		"Pages":    pages,
		"Imported": r.Form.Has("added"),
		"Added":    added,
		"Skipped":  r.FormValue("skipped"),
		"Assigned": r.FormValue("assigned"),
	})
}

func (a handlers) accountCreate(w http.ResponseWriter, r *http.Request) {
	b, _, ok, err := a.BudgetFor(r)
	if err != nil {
		a.Fail(w, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	name, valid := accountForm(r)
	if !valid {
		http.Error(w, "invalid account", http.StatusBadRequest)
		return
	}
	if _, err := a.DB().ExecContext(r.Context(), `INSERT INTO import_accounts (budget_id, name, format) VALUES (?, ?, '')`, b.ID, name); err != nil {
		a.Fail(w, err)
		return
	}
	http.Redirect(w, r, importURL("/import", b, nil), http.StatusSeeOther)
}

func (a handlers) accountUpdate(w http.ResponseWriter, r *http.Request) {
	acc, b, ok, err := a.accountFor(r, r.PathValue("id"))
	if err != nil {
		a.Fail(w, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	name, valid := accountForm(r)
	if !valid {
		http.Error(w, "invalid account", http.StatusBadRequest)
		return
	}
	section, line := "", ""
	if d := r.FormValue("default"); d != "" {
		var ignored, ok bool
		if section, line, ignored, ok = parseRuleTarget(d); !ok || ignored {
			http.Error(w, "invalid default line", http.StatusBadRequest)
			return
		}
	}
	ctx := r.Context()
	tx, err := a.DB().BeginTx(ctx, nil)
	if err != nil {
		a.Fail(w, err)
		return
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `UPDATE import_accounts SET name = ?, default_section = NULLIF(?, ''), default_line_name = NULLIF(?, '')
		WHERE id = ?`, name, section, line, acc.ID)
	if err == nil {
		_, err = applyRules(ctx, tx, b.ID)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		a.Fail(w, err)
		return
	}
	http.Redirect(w, r, importURL("/import", b, nil), http.StatusSeeOther)
}

// accountDelete deletes the account with its imports and transactions.
func (a handlers) accountDelete(w http.ResponseWriter, r *http.Request) {
	acc, b, ok, err := a.accountFor(r, r.PathValue("id"))
	if err != nil {
		a.Fail(w, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	if _, err := a.DB().ExecContext(r.Context(), `DELETE FROM import_accounts WHERE id = ?`, acc.ID); err != nil {
		a.Fail(w, err)
		return
	}
	http.Redirect(w, r, importURL("/import", b, nil), http.StatusSeeOther)
}

// bankFileRows reads the file for acc in the bank format that fits it, the
// account's last one first, and marks which rows are imported already.
func (a handlers) bankFileRows(ctx context.Context, q app.Querier, acc account, data []byte) (format string, rows []bankRow, imported []bool, err error) {
	f, known := bankFormatByID(acc.Format)
	if fits := bankFormatsFor(data); len(fits) > 0 && !slices.Contains(fits, f) {
		f = fits[0]
	} else if !known {
		f = bankFormats[0] // none fits: its errors explain why
	}
	if rows, err = parseBankCSV(data, f); err != nil {
		return "", nil, nil, err
	}
	from, to := rows[0].Booked, rows[0].Booked
	for _, r := range rows {
		if r.Booked.Before(from) {
			from = r.Booked
		}
		if r.Booked.After(to) {
			to = r.Booked
		}
	}
	have := map[string]bool{}
	res, err := q.QueryContext(ctx, `SELECT booked, text, amount, seq FROM import_transactions
		WHERE account_id = ? AND booked BETWEEN ? AND ?`, acc.ID, from, to)
	if err != nil {
		return "", nil, nil, err
	}
	defer res.Close()
	for res.Next() {
		var r bankRow
		if err := res.Scan(&r.Booked, &r.Text, &r.Amount, &r.Seq); err != nil {
			return "", nil, nil, err
		}
		have[r.key()+"\x00"+strconv.Itoa(r.Seq)] = true
	}
	for _, r := range rows {
		imported = append(imported, have[r.key()+"\x00"+strconv.Itoa(r.Seq)])
	}
	return f.ID, rows, imported, res.Err()
}

// previewPage reads an uploaded file and shows its rows before anything is imported.
func (a handlers) previewPage(w http.ResponseWriter, r *http.Request) {
	L := a.Locale(r)
	r.Body = http.MaxBytesReader(w, r.Body, maxReceiptFile+64<<10)
	if err := r.ParseMultipartForm(maxBankFile); err != nil {
		http.Error(w, L.T("import.err_too_large"), http.StatusRequestEntityTooLarge)
		return
	}
	acc, b, ok, err := a.accountFor(r, r.FormValue("account"))
	if err != nil {
		a.Fail(w, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		http.Error(w, L.T("import.err_no_file"), http.StatusBadRequest)
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxReceiptFile+1))
	if err != nil {
		a.Fail(w, err)
		return
	}
	if len(data) > maxReceiptFile {
		http.Error(w, L.T("import.err_too_large"), http.StatusRequestEntityTooLarge)
		return
	}
	if f, receipts, err := parseReceipts(data); err == nil {
		a.receiptPreview(w, r, acc, b, f, receipts)
		return
	}
	if len(data) > maxBankFile {
		http.Error(w, L.T("import.err_too_large"), http.StatusRequestEntityTooLarge)
		return
	}

	page := map[string]any{
		"Title":   L.T("import.preview"),
		"Current": b,
		"Account": acc,
		"Back":    importURL("/import", b, nil),
	}
	format, rows, imported, err := a.bankFileRows(r.Context(), a.DB(), acc, data)
	var pe *bankParseError
	if errors.As(err, &pe) {
		page["Error"] = pe
		a.Render(w, r, "import_preview", page)
		return
	}
	if err != nil {
		a.Fail(w, err)
		return
	}
	f, _ := bankFormatByID(format)
	page["Format"] = f.Name
	type previewRow struct {
		bankRow
		Imported bool
	}
	var list []previewRow
	added := 0
	for i, row := range rows {
		list = append(list, previewRow{row, imported[i]})
		if !imported[i] {
			added++
		}
	}
	page["Rows"] = list
	page["Added"] = added
	page["Skipped"] = len(rows) - added
	page["Data"] = base64.StdEncoding.EncodeToString(data)
	a.Render(w, r, "import_preview", page)
}

// importConfirm adds the rows of a previewed file that are not imported yet.
func (a handlers) importConfirm(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	acc, b, ok, err := a.accountFor(r, r.FormValue("account"))
	if err != nil {
		a.Fail(w, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	if r.Form.Has("receipts") {
		var receipts []receipt
		if json.Unmarshal([]byte(r.FormValue("receipts")), &receipts) != nil || slices.ContainsFunc(receipts, func(rc receipt) bool { return !rc.valid() }) {
			http.Error(w, "invalid receipts", http.StatusBadRequest)
			return
		}
		a.receiptImport(w, r, acc, b, receipts)
		return
	}
	data, err := base64.StdEncoding.DecodeString(r.FormValue("data"))
	if err != nil || len(data) > maxBankFile {
		http.Error(w, "invalid file", http.StatusBadRequest)
		return
	}

	tx, err := a.DB().BeginTx(ctx, nil)
	if err != nil {
		a.Fail(w, err)
		return
	}
	defer tx.Rollback()
	// Lock the account so two imports of the same file cannot both add its rows.
	if err := tx.QueryRowContext(ctx, `SELECT id FROM import_accounts WHERE id = ? FOR UPDATE`, acc.ID).Scan(new(int64)); err != nil {
		a.Fail(w, err)
		return
	}
	format, rows, imported, err := a.bankFileRows(ctx, tx, acc, data)
	if err != nil {
		http.Error(w, "invalid file", http.StatusBadRequest)
		return
	}
	if _, err := tx.ExecContext(ctx, `UPDATE import_accounts SET format = ? WHERE id = ?`, format, acc.ID); err != nil {
		a.Fail(w, err)
		return
	}
	added := 0
	for _, done := range imported {
		if !done {
			added++
		}
	}
	if added > 0 {
		var user any
		if u := app.CurrentUser(r); u != nil && u.ID != 0 {
			user = u.ID
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO import_imports (account_id, user_id, imported_at, added, skipped) VALUES (?, ?, ?, ?, ?)`,
			acc.ID, user, time.Now().UTC(), added, len(rows)-added)
		if err != nil {
			a.Fail(w, err)
			return
		}
		importID, err := res.LastInsertId()
		if err != nil {
			a.Fail(w, err)
			return
		}
		for i, row := range rows {
			if imported[i] {
				continue
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO import_transactions (account_id, import_id, booked, text, amount, seq) VALUES (?, ?, ?, ?, ?, ?)`,
				acc.ID, importID, row.Booked, row.Text, row.Amount, row.Seq); err != nil {
				a.Fail(w, err)
				return
			}
		}
	}
	assigned, err := applyRules(ctx, tx, acc.BudgetID)
	if err != nil {
		a.Fail(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		a.Fail(w, err)
		return
	}
	http.Redirect(w, r, importURL("/import", b, url.Values{"added": {strconv.Itoa(added)}, "skipped": {strconv.Itoa(len(rows) - added)},
		"assigned": {strconv.Itoa(assigned)}}), http.StatusSeeOther)
}

// importDelete undoes an import: its transactions are deleted with it.
func (a handlers) importDelete(w http.ResponseWriter, r *http.Request) {
	var account int64
	err := a.DB().QueryRowContext(r.Context(), `SELECT account_id FROM import_imports WHERE id = ?`, r.PathValue("id")).Scan(&account)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		a.Fail(w, err)
		return
	}
	acc, b, ok, err := a.accountFor(r, strconv.FormatInt(account, 10))
	if err != nil {
		a.Fail(w, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	if _, err := a.DB().ExecContext(r.Context(), `DELETE FROM import_imports WHERE id = ? AND account_id = ?`, r.PathValue("id"), acc.ID); err != nil {
		a.Fail(w, err)
		return
	}
	http.Redirect(w, r, importURL("/import", b, nil), http.StatusSeeOther)
}
