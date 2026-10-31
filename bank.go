package main

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// The bank plugin imports transactions from the banks' CSV exports into a
// budget's bank accounts. The file is read twice: once for a preview that
// marks the rows already imported, and again, sent back with the preview's
// form, when the import is confirmed. It is never stored.
type bankPlugin struct{}

func init() { register(bankPlugin{}) }

func (bankPlugin) ID() string { return "bank" }

func (bankPlugin) Migrations() fs.FS {
	m, _ := fs.Sub(embedded, "migrations/bank")
	return m
}

func (bankPlugin) Routes(mux *http.ServeMux, a *App) {
	mux.HandleFunc("GET /bank", a.bankPage)
	mux.HandleFunc("POST /bank/accounts", a.only(roleEditor, a.bankAccountCreate))
	mux.HandleFunc("POST /bank/accounts/{id}", a.only(roleEditor, a.bankAccountUpdate))
	mux.HandleFunc("POST /bank/accounts/{id}/delete", a.only(roleEditor, a.bankAccountDelete))
	mux.HandleFunc("POST /bank/preview", a.only(roleEditor, a.bankPreview))
	mux.HandleFunc("POST /bank/import", a.only(roleEditor, a.bankImport))
	mux.HandleFunc("POST /bank/imports/{id}/delete", a.only(roleEditor, a.bankImportDelete))
	mux.HandleFunc("GET /bank/transactions", a.bankTransactionsPage)
	mux.HandleFunc("POST /bank/transactions", a.only(roleEditor, a.bankTransactionsSave))
	mux.HandleFunc("GET /bank/rules", a.bankRulesPage)
	mux.HandleFunc("POST /bank/rules", a.only(roleEditor, a.bankRulesSave))
	mux.HandleFunc("GET /bank/compare", a.bankComparePage)
	mux.HandleFunc("GET /bank/fill", a.only(roleEditor, a.bankFillPage))
	mux.HandleFunc("POST /bank/fill", a.only(roleEditor, a.bankFillSave))
}

func (bankPlugin) Nav() []NavItem { return []NavItem{{Path: "/bank", Label: "nav.bank"}} }

const maxBankFile = 2 << 20 // bytes; an export of a year is far smaller

type bankAccount struct {
	ID           int64
	BudgetID     int64
	Name         string
	Format       string
	Transactions int
}

func (b bankAccount) FormatName() string {
	f, _ := bankFormatByID(b.Format)
	return f.Name
}

// bankURL is the plugin's page at path for budget b, with query q.
func bankURL(path string, b budgetRef, q url.Values) string {
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

type bankTab struct {
	URL, Label string
	Active     bool
}

// bankTabs are the plugin's pages for budget b, with active marked.
func bankTabs(L *Locale, b budgetRef, active string) []bankTab {
	var tabs []bankTab
	for _, t := range []struct{ key, path string }{{"import", "/bank"}, {"transactions", "/bank/transactions"}, {"rules", "/bank/rules"}, {"compare", "/bank/compare"}} {
		tabs = append(tabs, bankTab{bankURL(t.path, b, nil), L.T("bank.tab_" + t.key), t.key == active})
	}
	return tabs
}

func (a *App) bankAccounts(ctx context.Context, budget int64) ([]bankAccount, error) {
	rows, err := a.db.QueryContext(ctx, `SELECT a.id, a.budget_id, a.name, a.format,
			(SELECT COUNT(*) FROM bank_transactions t WHERE t.account_id = a.id)
		FROM bank_accounts a WHERE a.budget_id = ? ORDER BY a.name`, budget)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []bankAccount
	for rows.Next() {
		var b bankAccount
		if err := rows.Scan(&b.ID, &b.BudgetID, &b.Name, &b.Format, &b.Transactions); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// bankAccountFor is the account with the given id, and its budget, when the
// user may see that budget; ok is false otherwise.
func (a *App) bankAccountFor(r *http.Request, id string) (acc bankAccount, b budgetRef, ok bool, err error) {
	err = a.db.QueryRowContext(r.Context(), `SELECT id, budget_id, name, format FROM bank_accounts WHERE id = ?`, id).
		Scan(&acc.ID, &acc.BudgetID, &acc.Name, &acc.Format)
	if errors.Is(err, sql.ErrNoRows) {
		return acc, b, false, nil
	}
	if err != nil {
		return acc, b, false, err
	}
	visible, err := a.visibleBudgets(r.Context(), currentUser(r))
	for _, v := range visible {
		if v.ID == acc.BudgetID {
			return acc, v, true, err
		}
	}
	return acc, b, false, err
}

// bankAccountForm reads and checks an account's name and format.
func bankAccountForm(r *http.Request) (name, format string, ok bool) {
	name = strings.TrimSpace(r.FormValue("name"))
	format = r.FormValue("format")
	_, known := bankFormatByID(format)
	return name, format, known && name != "" && utf8.RuneCountInString(name) <= 100
}

type bankImportRow struct {
	ID         int64
	Account    string
	User       string
	ImportedAt time.Time
	Added      int
	Skipped    int
	Left       int // of the added, those not deleted since
}

// bankPage lists the chosen budget's accounts and imports, with the form to import a file.
func (a *App) bankPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	current, visible, ok, err := a.chosenBudget(r)
	if err != nil {
		a.fail(w, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	accounts, err := a.bankAccounts(ctx, current.ID)
	if err != nil {
		a.fail(w, err)
		return
	}

	var imports []bankImportRow
	rows, err := a.db.QueryContext(ctx, `SELECT i.id, a.name, COALESCE(u.name, ''), i.imported_at, i.added, i.skipped,
			(SELECT COUNT(*) FROM bank_transactions t WHERE t.import_id = i.id)
		FROM bank_imports i JOIN bank_accounts a ON a.id = i.account_id LEFT JOIN users u ON u.id = i.user_id
		WHERE a.budget_id = ? ORDER BY i.id DESC LIMIT 20`, current.ID)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var i bankImportRow
		if err := rows.Scan(&i.ID, &i.Account, &i.User, &i.ImportedAt, &i.Added, &i.Skipped, &i.Left); err != nil {
			a.fail(w, err)
			return
		}
		imports = append(imports, i)
	}
	if err := rows.Err(); err != nil {
		a.fail(w, err)
		return
	}

	added, _ := strconv.Atoi(r.FormValue("added"))
	L := a.locale(r)
	a.render(w, r, "bank", map[string]any{
		"Title":    L.T("nav.bank"),
		"Tabs":     bankTabs(L, current, "import"),
		"Current":  current,
		"Budgets":  pickable(visible),
		"Accounts": accounts,
		"Formats":  bankFormats,
		"Imports":  imports,
		"Imported": r.Form.Has("added"),
		"Added":    added,
		"Skipped":  r.FormValue("skipped"),
		"Assigned": r.FormValue("assigned"),
	})
}

func (a *App) bankAccountCreate(w http.ResponseWriter, r *http.Request) {
	b, _, ok, err := a.budgetFor(r)
	if err != nil {
		a.fail(w, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	name, format, valid := bankAccountForm(r)
	if !valid {
		http.Error(w, "invalid account", http.StatusBadRequest)
		return
	}
	if _, err := a.db.ExecContext(r.Context(), `INSERT INTO bank_accounts (budget_id, name, format) VALUES (?, ?, ?)`, b.ID, name, format); err != nil {
		a.fail(w, err)
		return
	}
	http.Redirect(w, r, bankURL("/bank", b, nil), http.StatusSeeOther)
}

func (a *App) bankAccountUpdate(w http.ResponseWriter, r *http.Request) {
	acc, b, ok, err := a.bankAccountFor(r, r.PathValue("id"))
	if err != nil {
		a.fail(w, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	name, format, valid := bankAccountForm(r)
	if !valid {
		http.Error(w, "invalid account", http.StatusBadRequest)
		return
	}
	if _, err := a.db.ExecContext(r.Context(), `UPDATE bank_accounts SET name = ?, format = ? WHERE id = ?`, name, format, acc.ID); err != nil {
		a.fail(w, err)
		return
	}
	http.Redirect(w, r, bankURL("/bank", b, nil), http.StatusSeeOther)
}

// bankAccountDelete deletes the account with its imports and transactions.
func (a *App) bankAccountDelete(w http.ResponseWriter, r *http.Request) {
	acc, b, ok, err := a.bankAccountFor(r, r.PathValue("id"))
	if err != nil {
		a.fail(w, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	if _, err := a.db.ExecContext(r.Context(), `DELETE FROM bank_accounts WHERE id = ?`, acc.ID); err != nil {
		a.fail(w, err)
		return
	}
	http.Redirect(w, r, bankURL("/bank", b, nil), http.StatusSeeOther)
}

// bankFileRows reads the file for acc and marks which rows are imported already.
func (a *App) bankFileRows(ctx context.Context, q querier, acc bankAccount, data []byte) (rows []bankRow, imported []bool, err error) {
	f, _ := bankFormatByID(acc.Format)
	if rows, err = parseBankCSV(data, f); err != nil {
		return nil, nil, err
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
	res, err := q.QueryContext(ctx, `SELECT booked, text, amount, seq FROM bank_transactions
		WHERE account_id = ? AND booked BETWEEN ? AND ?`, acc.ID, from, to)
	if err != nil {
		return nil, nil, err
	}
	defer res.Close()
	for res.Next() {
		var r bankRow
		if err := res.Scan(&r.Booked, &r.Text, &r.Amount, &r.Seq); err != nil {
			return nil, nil, err
		}
		have[r.key()+"\x00"+strconv.Itoa(r.Seq)] = true
	}
	for _, r := range rows {
		imported = append(imported, have[r.key()+"\x00"+strconv.Itoa(r.Seq)])
	}
	return rows, imported, res.Err()
}

// bankPreview reads an uploaded file and shows its rows before anything is imported.
func (a *App) bankPreview(w http.ResponseWriter, r *http.Request) {
	L := a.locale(r)
	r.Body = http.MaxBytesReader(w, r.Body, maxBankFile+64<<10)
	if err := r.ParseMultipartForm(maxBankFile); err != nil {
		http.Error(w, L.T("bank.err_too_large"), http.StatusRequestEntityTooLarge)
		return
	}
	acc, b, ok, err := a.bankAccountFor(r, r.FormValue("account"))
	if err != nil {
		a.fail(w, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		http.Error(w, L.T("bank.err_no_file"), http.StatusBadRequest)
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxBankFile+1))
	if err != nil {
		a.fail(w, err)
		return
	}
	if len(data) > maxBankFile {
		http.Error(w, L.T("bank.err_too_large"), http.StatusRequestEntityTooLarge)
		return
	}

	page := map[string]any{
		"Title":   L.T("bank.preview"),
		"Current": b,
		"Account": acc,
		"Back":    bankURL("/bank", b, nil),
	}
	rows, imported, err := a.bankFileRows(r.Context(), a.db, acc, data)
	var pe *bankParseError
	if errors.As(err, &pe) {
		page["Error"] = pe
		page["Fits"] = bankFormatsFor(data)
		a.render(w, r, "bank_preview", page)
		return
	}
	if err != nil {
		a.fail(w, err)
		return
	}
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
	a.render(w, r, "bank_preview", page)
}

// bankImport adds the rows of a previewed file that are not imported yet.
func (a *App) bankImport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	acc, b, ok, err := a.bankAccountFor(r, r.FormValue("account"))
	if err != nil {
		a.fail(w, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	data, err := base64.StdEncoding.DecodeString(r.FormValue("data"))
	if err != nil || len(data) > maxBankFile {
		http.Error(w, "invalid file", http.StatusBadRequest)
		return
	}

	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer tx.Rollback()
	// Lock the account so two imports of the same file cannot both add its rows.
	if err := tx.QueryRowContext(ctx, `SELECT id FROM bank_accounts WHERE id = ? FOR UPDATE`, acc.ID).Scan(new(int64)); err != nil {
		a.fail(w, err)
		return
	}
	rows, imported, err := a.bankFileRows(ctx, tx, acc, data)
	if err != nil {
		http.Error(w, "invalid file", http.StatusBadRequest)
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
		if u := currentUser(r); u != nil && u.ID != 0 {
			user = u.ID
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO bank_imports (account_id, user_id, imported_at, added, skipped) VALUES (?, ?, ?, ?, ?)`,
			acc.ID, user, time.Now().UTC(), added, len(rows)-added)
		if err != nil {
			a.fail(w, err)
			return
		}
		importID, err := res.LastInsertId()
		if err != nil {
			a.fail(w, err)
			return
		}
		for i, row := range rows {
			if imported[i] {
				continue
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO bank_transactions (account_id, import_id, booked, text, amount, seq) VALUES (?, ?, ?, ?, ?, ?)`,
				acc.ID, importID, row.Booked, row.Text, row.Amount, row.Seq); err != nil {
				a.fail(w, err)
				return
			}
		}
	}
	assigned, err := applyBankRules(ctx, tx, acc.BudgetID)
	if err != nil {
		a.fail(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		a.fail(w, err)
		return
	}
	http.Redirect(w, r, bankURL("/bank", b, url.Values{"added": {strconv.Itoa(added)}, "skipped": {strconv.Itoa(len(rows) - added)},
		"assigned": {strconv.Itoa(assigned)}}), http.StatusSeeOther)
}

// bankImportDelete undoes an import: its transactions are deleted with it.
func (a *App) bankImportDelete(w http.ResponseWriter, r *http.Request) {
	var account int64
	err := a.db.QueryRowContext(r.Context(), `SELECT account_id FROM bank_imports WHERE id = ?`, r.PathValue("id")).Scan(&account)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		a.fail(w, err)
		return
	}
	acc, b, ok, err := a.bankAccountFor(r, strconv.FormatInt(account, 10))
	if err != nil {
		a.fail(w, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	if _, err := a.db.ExecContext(r.Context(), `DELETE FROM bank_imports WHERE id = ? AND account_id = ?`, r.PathValue("id"), acc.ID); err != nil {
		a.fail(w, err)
		return
	}
	http.Redirect(w, r, bankURL("/bank", b, nil), http.StatusSeeOther)
}
