package app

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-sql-driver/mysql"
)

// budgetAdmin is a budget as the manage page edits it.
type budgetAdmin struct {
	ID        int64
	Name      string
	Slug      string
	IsDefault bool
	Everyone  bool
	Owner     int64
	Members   map[int64]bool
	Groups    string // comma-separated
	Years     int
}

// NamePattern is the name as an HTML pattern, for typing it to confirm.
func (b budgetAdmin) NamePattern() string { return regexp.QuoteMeta(b.Name) }

type userOption struct {
	ID   int64
	Name string
}

func (a *App) budgetsAdmin(ctx context.Context) ([]budgetAdmin, []userOption, error) {
	rows, err := a.db.QueryContext(ctx, `SELECT b.id, b.name, b.slug, b.is_default, b.everyone, COALESCE(b.owner_id, 0),
			(SELECT COUNT(*) FROM budget_years y WHERE y.budget_id = b.id),
			COALESCE((SELECT GROUP_CONCAT(g.name ORDER BY g.name SEPARATOR ', ') FROM budget_groups g WHERE g.budget_id = b.id), '')
		FROM budgets b ORDER BY b.is_default DESC, b.name`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var budgets []budgetAdmin
	byID := map[int64]*budgetAdmin{}
	for rows.Next() {
		b := budgetAdmin{Members: map[int64]bool{}}
		if err := rows.Scan(&b.ID, &b.Name, &b.Slug, &b.IsDefault, &b.Everyone, &b.Owner, &b.Years, &b.Groups); err != nil {
			return nil, nil, err
		}
		budgets = append(budgets, b)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	for i := range budgets {
		byID[budgets[i].ID] = &budgets[i]
	}
	members, err := a.db.QueryContext(ctx, "SELECT budget_id, user_id FROM budget_members")
	if err != nil {
		return nil, nil, err
	}
	defer members.Close()
	for members.Next() {
		var b, u int64
		if err := members.Scan(&b, &u); err != nil {
			return nil, nil, err
		}
		if x := byID[b]; x != nil {
			x.Members[u] = true
		}
	}
	if err := members.Err(); err != nil {
		return nil, nil, err
	}

	users, err := a.db.QueryContext(ctx, "SELECT id, COALESCE(NULLIF(name, ''), NULLIF(email, ''), subject) AS n FROM users ORDER BY n")
	if err != nil {
		return nil, nil, err
	}
	defer users.Close()
	var opts []userOption
	for users.Next() {
		var o userOption
		if err := users.Scan(&o.ID, &o.Name); err != nil {
			return nil, nil, err
		}
		opts = append(opts, o)
	}
	return budgets, opts, users.Err()
}

var slugPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// validSlug is a budget address: lowercase letters, digits and dashes, and not
// only digits, which would be read as a year.
func validSlug(s string) bool {
	return len(s) <= 100 && slugPattern.MatchString(s) && strings.Trim(s, "0123456789") != ""
}

// slugify makes an address from a budget name, e.g. "Fælles ferie" → "faelles-ferie".
func slugify(name string) string {
	r := strings.NewReplacer("æ", "ae", "ø", "oe", "å", "aa", "ä", "ae", "ö", "oe", "ü", "ue", "ß", "ss", "é", "e", "è", "e", "á", "a")
	var b strings.Builder
	dash := false
	for _, c := range r.Replace(strings.ToLower(name)) {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
			b.WriteRune(c)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	s := strings.TrimSuffix(b.String(), "-")
	if !validSlug(s) {
		s = strings.Trim("budget-"+s, "-")
	}
	return s
}

// manageCreateBudget creates a private budget owned by the admin creating it.
func (a *App) manageCreateBudget(w http.ResponseWriter, r *http.Request) {
	L := a.Locale(r)
	name := strings.TrimSpace(r.PostFormValue("name"))
	slug := strings.TrimSpace(r.PostFormValue("slug"))
	if slug == "" {
		slug = slugify(name)
	}
	if name == "" || len(name) > 100 || !validSlug(slug) {
		http.Error(w, L.T("manage.budget_invalid"), http.StatusBadRequest)
		return
	}
	_, err := a.db.ExecContext(r.Context(), "INSERT INTO budgets (name, slug, owner_id) VALUES (?, ?, ?)", name, slug, CurrentUser(r).ID)
	if isDuplicate(err) {
		http.Error(w, L.T("manage.slug_taken", slug), http.StatusBadRequest)
		return
	}
	if err != nil {
		a.Fail(w, err)
		return
	}
	http.Redirect(w, r, "/manage?budget="+slug+"#budgets", http.StatusSeeOther)
}

// manageUpdateBudget saves a budget's name, address, owner and who may see it.
func (a *App) manageUpdateBudget(w http.ResponseWriter, r *http.Request) {
	L := a.Locale(r)
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	name, slug := strings.TrimSpace(r.PostFormValue("name")), strings.TrimSpace(r.PostFormValue("slug"))
	if name == "" || len(name) > 100 || !validSlug(slug) {
		http.Error(w, L.T("manage.budget_invalid"), http.StatusBadRequest)
		return
	}
	var owner any // NULL for no owner
	if o, err := strconv.ParseInt(r.PostFormValue("owner"), 10, 64); err == nil {
		owner = o
	}
	ctx := r.Context()
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		a.Fail(w, err)
		return
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, "UPDATE budgets SET name = ?, slug = ?, owner_id = ?, everyone = ? WHERE id = ?",
		name, slug, owner, r.PostFormValue("everyone") != "", id)
	if isDuplicate(err) {
		http.Error(w, L.T("manage.slug_taken", slug), http.StatusBadRequest)
		return
	}
	if err == nil {
		var n int64
		if n, err = res.RowsAffected(); err == nil && n == 0 {
			// MariaDB reports 0 for an unchanged row too, so check that the budget exists.
			err = tx.QueryRowContext(ctx, "SELECT id FROM budgets WHERE id = ?", id).Scan(&id)
		}
	}
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	exec := func(q string, args ...any) {
		if err == nil {
			_, err = tx.ExecContext(ctx, q, args...)
		}
	}
	exec("DELETE FROM budget_members WHERE budget_id = ?", id)
	for _, m := range r.PostForm["member"] {
		exec("INSERT IGNORE INTO budget_members (budget_id, user_id) SELECT ?, id FROM users WHERE id = ?", id, m)
	}
	exec("DELETE FROM budget_groups WHERE budget_id = ?", id)
	for _, g := range splitList(r.PostFormValue("groups")) {
		exec("INSERT IGNORE INTO budget_groups (budget_id, name) VALUES (?, ?)", id, strings.TrimPrefix(g, "/"))
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		a.Fail(w, err)
		return
	}
	http.Redirect(w, r, "/manage?budget="+slug+"#budgets", http.StatusSeeOther)
}

// manageDeleteBudget deletes a budget with all its years once its name has
// been typed to confirm. The default budget stays, as its addresses do.
func (a *App) manageDeleteBudget(w http.ResponseWriter, r *http.Request) {
	L := a.Locale(r)
	var name string
	var isDefault bool
	err := a.db.QueryRowContext(r.Context(), "SELECT name, is_default FROM budgets WHERE id = ?", r.PathValue("id")).Scan(&name, &isDefault)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		a.Fail(w, err)
		return
	}
	if isDefault {
		http.Error(w, L.T("manage.default_kept"), http.StatusBadRequest)
		return
	}
	if r.PostFormValue("confirm") != name {
		http.Error(w, L.T("manage.delete_budget_wrong", name), http.StatusBadRequest)
		return
	}
	if _, err := a.db.ExecContext(r.Context(), "DELETE FROM budgets WHERE id = ?", r.PathValue("id")); err != nil {
		a.Fail(w, err)
		return
	}
	http.Redirect(w, r, "/manage#budgets", http.StatusSeeOther)
}

func isDuplicate(err error) bool {
	var me *mysql.MySQLError
	return errors.As(err, &me) && me.Number == 1062
}

// openBudget is the budget whose settings the manage page shows open: one that
// was chosen or just created or saved, none on a plain visit.
func openBudget(r *http.Request, current BudgetRef) int64 {
	if r.FormValue("budget") == "" {
		return 0
	}
	return current.ID
}

// manageURL is the manage page for budget b, a year (0 for none) and a section anchor.
func manageURL(b BudgetRef, year int, anchor string) string {
	q := url.Values{}
	if !b.IsDefault {
		q.Set("budget", b.Slug)
	}
	if year != 0 {
		q.Set("year", strconv.Itoa(year))
	}
	if len(q) == 0 {
		return "/manage" + anchor
	}
	return "/manage?" + q.Encode() + anchor
}
