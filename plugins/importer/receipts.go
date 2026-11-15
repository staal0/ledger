package importer

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/staal0/ledger/internal/app"
)

// maxReceiptFile is the largest receipt export; it holds every receipt ever, with images.
const maxReceiptFile = 64 << 20

// receipt is one purchase from a receipt export; Total and the items' amounts are positive for a purchase.
type receipt struct {
	ID     string
	Booked time.Time
	Store  string
	Total  app.Money
	Items  []receiptItem
}

// Amount is the receipt as the bank has it: negative for a purchase.
func (rc receipt) Amount() app.Money { return -rc.Total }

type receiptItem struct {
	ID     int64 `json:"-"`
	Name   string
	Amount app.Money
}

var errNoReceipts = errors.New("not a receipt export")

func money(f float64) app.Money { return app.Money(math.Round(f * 100)) }

// cut keeps s within n runes.
func cut(s string, n int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > n {
		s = string([]rune(s)[:n])
	}
	return s
}

// receiptsSeen marks the receipts imported into the account before, or met earlier in the file.
func receiptsSeen(ctx context.Context, q app.Querier, account int64, receipts []receipt) ([]bool, error) {
	have := map[string]bool{}
	rows, err := q.QueryContext(ctx, `SELECT receipt FROM import_transactions WHERE account_id = ? AND receipt IS NOT NULL`, account)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		have[id] = true
	}
	seen := make([]bool, len(receipts))
	for i, rc := range receipts {
		seen[i] = have[rc.ID]
		have[rc.ID] = true
	}
	return seen, rows.Err()
}

// receiptPreview shows a receipt export before it is imported: which receipts
// are new, and which bank rows they cover and so will be ignored.
func (a handlers) receiptPreview(w http.ResponseWriter, r *http.Request, acc account, b app.BudgetRef, f receiptFormat, receipts []receipt) {
	ctx := r.Context()
	seen, err := receiptsSeen(ctx, a.DB(), acc.ID, receipts)
	if err != nil {
		a.Fail(w, err)
		return
	}
	// The bank rows no receipt or choice by hand has yet.
	bank := map[app.Money][]*bankCandidate{}
	rows, err := a.DB().QueryContext(ctx, `SELECT t.id, t.booked, t.amount, CONCAT(a.name, ': ', t.text) FROM import_transactions t
		JOIN import_accounts a ON a.id = t.account_id
		WHERE a.budget_id = ? AND t.receipt IS NULL AND NOT t.split AND NOT t.by_receipt
			AND (t.rule_id IS NOT NULL AND NOT t.ignored OR t.by_default OR t.line_id IS NULL AND NOT t.ignored)
		ORDER BY t.booked, t.id`, acc.BudgetID)
	if err != nil {
		a.Fail(w, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var c bankCandidate
		var amount app.Money
		if err := rows.Scan(&c.ID, &c.Booked, &amount, &c.Text); err != nil {
			a.Fail(w, err)
			return
		}
		bank[amount] = append(bank[amount], &c)
	}
	if err := rows.Err(); err != nil {
		a.Fail(w, err)
		return
	}
	type previewReceipt struct {
		receipt
		Imported bool
		Covers   *bankCandidate
	}
	list := make([]previewReceipt, len(receipts))
	order := make([]int, 0, len(receipts))
	added := 0
	for i, rc := range receipts {
		list[i] = previewReceipt{receipt: rc, Imported: seen[i]}
		if !seen[i] {
			added++
			order = append(order, i)
		}
	}
	// Matched in date order, as the import does.
	slices.SortStableFunc(order, func(x, y int) int { return receipts[x].Booked.Compare(receipts[y].Booked) })
	for _, i := range order {
		list[i].Covers = coverFor(bank, receipts[i].Booked, -receipts[i].Total)
	}
	slices.SortStableFunc(list, func(x, y previewReceipt) int { return y.Booked.Compare(x.Booked) })
	data, err := json.Marshal(receipts)
	if err != nil {
		a.Fail(w, err)
		return
	}
	L := a.Locale(r)
	a.Render(w, r, "import_preview", map[string]any{
		"Title":    L.T("import.preview"),
		"Current":  b,
		"Account":  acc,
		"Back":     importURL("/import", b, nil),
		"Receipts": list,
		"Format":   f.Name,
		"Added":    added,
		"Skipped":  len(receipts) - added,
		"Data":     string(data),
	})
}

// valid checks a receipt sent back from the preview.
func (rc receipt) valid() bool {
	if rc.ID == "" || len(rc.ID) > 64 || rc.Store == "" || utf8.RuneCountInString(rc.Store) > maxBankText || rc.Booked.IsZero() {
		return false
	}
	for _, it := range rc.Items {
		if utf8.RuneCountInString(it.Name) > 255 {
			return false
		}
	}
	return true
}

// receiptImport adds the receipts of an export that are not imported yet; the export holds them all.
func (a handlers) receiptImport(w http.ResponseWriter, r *http.Request, acc account, b app.BudgetRef, receipts []receipt) {
	ctx := r.Context()
	tx, err := a.DB().BeginTx(ctx, nil)
	if err != nil {
		a.Fail(w, err)
		return
	}
	defer tx.Rollback()
	if err := tx.QueryRowContext(ctx, `SELECT id FROM import_accounts WHERE id = ? FOR UPDATE`, acc.ID).Scan(new(int64)); err != nil {
		a.Fail(w, err)
		return
	}
	seen, err := receiptsSeen(ctx, tx, acc.ID, receipts)
	if err != nil {
		a.Fail(w, err)
		return
	}
	var todo []receipt
	for i, rc := range receipts {
		if !seen[i] {
			todo = append(todo, rc)
		}
	}
	if len(todo) > 0 {
		var user any
		if u := app.CurrentUser(r); u != nil && u.ID != 0 {
			user = u.ID
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO import_imports (account_id, user_id, imported_at, added, skipped) VALUES (?, ?, ?, ?, ?)`,
			acc.ID, user, time.Now().UTC(), len(todo), len(receipts)-len(todo))
		if err != nil {
			a.Fail(w, err)
			return
		}
		importID, err := res.LastInsertId()
		if err != nil {
			a.Fail(w, err)
			return
		}
		for _, rc := range todo {
			if err := addReceipt(ctx, tx, acc.ID, importID, rc); err != nil {
				a.Fail(w, err)
				return
			}
		}
	}
	assigned, err := applyRules(ctx, tx, acc.BudgetID)
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		a.Fail(w, err)
		return
	}
	http.Redirect(w, r, importURL("/import", b, url.Values{"added": {strconv.Itoa(len(todo))}, "skipped": {strconv.Itoa(len(receipts) - len(todo))},
		"assigned": {strconv.Itoa(assigned)}}), http.StatusSeeOther)
}

func addReceipt(ctx context.Context, tx *sql.Tx, account, importID int64, rc receipt) error {
	res, err := tx.ExecContext(ctx, `INSERT INTO import_transactions (account_id, import_id, booked, text, amount, seq, receipt)
		SELECT ?, ?, ?, ?, ?, COALESCE(MAX(seq), 0) + 1, ? FROM import_transactions WHERE account_id = ? AND booked = ? AND amount = ? AND text = ?`,
		account, importID, rc.Booked, rc.Store, -rc.Total, rc.ID, account, rc.Booked, -rc.Total, rc.Store)
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	for _, it := range rc.Items {
		if _, err := tx.ExecContext(ctx, `INSERT INTO import_receipt_items (transaction_id, name, amount) VALUES (?, ?, ?)`, id, it.Name, it.Amount); err != nil {
			return err
		}
	}
	return nil
}

// receiptItems are a transaction's receipt items, if it is a receipt.
func receiptItems(ctx context.Context, q app.Querier, transaction int64) ([]receiptItem, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, name, amount FROM import_receipt_items WHERE transaction_id = ? ORDER BY id`, transaction)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []receiptItem
	for rows.Next() {
		var it receiptItem
		if err := rows.Scan(&it.ID, &it.Name, &it.Amount); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// resetByReceipt undoes what receipts did, so applyReceipts can redo it; it returns those transactions.
func resetByReceipt(ctx context.Context, tx *sql.Tx, budget int64) (map[int64]bool, error) {
	ids := map[int64]bool{}
	rows, err := tx.QueryContext(ctx, `SELECT t.id FROM import_transactions t JOIN import_accounts a ON a.id = t.account_id
		WHERE a.budget_id = ? AND t.by_receipt`, budget)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids[id] = true
	}
	if err := rows.Err(); err != nil || len(ids) == 0 {
		return ids, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE s FROM import_splits s JOIN import_transactions t ON t.id = s.transaction_id
		JOIN import_accounts a ON a.id = t.account_id WHERE a.budget_id = ? AND t.by_receipt`, budget); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE import_receipt_items i JOIN import_transactions t ON t.id = i.transaction_id
		JOIN import_accounts a ON a.id = t.account_id SET i.line_id = NULL WHERE a.budget_id = ? AND t.by_receipt`, budget); err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE import_transactions t JOIN import_accounts a ON a.id = t.account_id
		SET t.split = FALSE, t.line_id = NULL, t.ignored = FALSE, t.rule_id = NULL, t.by_default = FALSE, t.by_receipt = FALSE
		WHERE a.budget_id = ? AND t.by_receipt`, budget)
	return ids, err
}

// bankCandidate is a bank row a receipt may cover.
type bankCandidate struct {
	ID     int64
	Booked time.Time
	Text   string
	taken  bool
}

// coverFor takes the bank row a receipt from day covers, by amount: the bank
// books a purchase from a day before to five days after, the nearest first.
func coverFor(bank map[app.Money][]*bankCandidate, day time.Time, amount app.Money) *bankCandidate {
	var best *bankCandidate
	off := func(c *bankCandidate) float64 { return math.Abs(c.Booked.Sub(day).Hours() / 24) }
	for _, c := range bank[amount] {
		days := c.Booked.Sub(day).Hours() / 24
		if days < -1 || days > 5 || c.taken {
			continue
		}
		if best == nil || off(c) < off(best) {
			best = c
		}
	}
	if best != nil {
		best.taken = true
	}
	return best
}

// applyReceipts lets receipts take over from rules and default lines: a bank
// transaction a receipt covers (same amount, a few days later at most) is
// ignored, and a receipt is split by rules for its items, the rest staying on
// the receipt's own line.
func applyReceipts(ctx context.Context, tx *sql.Tx, budget int64, rules []textRule, lines map[lineKey]int64) error {
	type row struct {
		id      int64
		booked  time.Time
		amount  app.Money
		line    int64
		ignored bool
		receipt bool
		free    bool // not set by hand, so a receipt may take over
	}
	rows, err := tx.QueryContext(ctx, `SELECT t.id, t.booked, t.amount, COALESCE(t.line_id, 0), t.ignored, t.receipt IS NOT NULL,
			t.rule_id IS NOT NULL AND NOT t.ignored OR t.by_default OR t.line_id IS NULL AND NOT t.ignored
		FROM import_transactions t JOIN import_accounts a ON a.id = t.account_id
		WHERE a.budget_id = ? AND NOT t.split ORDER BY t.booked, t.id`, budget)
	if err != nil {
		return err
	}
	defer rows.Close()
	var receipts []row
	bank := map[app.Money][]*bankCandidate{}
	for rows.Next() {
		var o row
		if err := rows.Scan(&o.id, &o.booked, &o.amount, &o.line, &o.ignored, &o.receipt, &o.free); err != nil {
			return err
		}
		if o.receipt {
			receipts = append(receipts, o)
		} else if o.free {
			bank[o.amount] = append(bank[o.amount], &bankCandidate{ID: o.id, Booked: o.booked})
		}
	}
	if err := rows.Err(); err != nil || len(receipts) == 0 {
		return err
	}

	for _, rc := range receipts {
		if rc.ignored {
			continue
		}
		if c := coverFor(bank, rc.booked, rc.amount); c != nil {
			if _, err := tx.ExecContext(ctx, `UPDATE import_transactions SET line_id = NULL, ignored = TRUE, rule_id = NULL, by_default = FALSE, by_receipt = TRUE
				WHERE id = ?`, c.ID); err != nil {
				return err
			}
		}
	}

	type part struct {
		line    int64
		ignored bool
		amount  app.Money
		items   []any // their ids
	}
	for _, rc := range receipts {
		if !rc.free {
			continue
		}
		items, err := receiptItems(ctx, tx, rc.id)
		if err != nil {
			return err
		}
		var parts []part
		var sum app.Money
		for _, it := range items {
			p := part{line: rc.line, amount: -it.Amount, items: []any{it.ID}}
			if r := matchRule(rules, it.Name); r != nil {
				if r.Ignored {
					p.line, p.ignored = 0, true
				} else if id, found := lines[lineKey{rc.booked.Year(), r.Section, r.LineName}]; found {
					p.line = id
				}
			}
			if p.line == 0 && !p.ignored {
				parts = nil // an item with nowhere to go: leave the receipt whole
				break
			}
			sum += p.amount
			merged := false
			for i := range parts {
				if parts[i].line == p.line && parts[i].ignored == p.ignored {
					parts[i].amount += p.amount
					parts[i].items = append(parts[i].items, it.ID)
					merged = true
				}
			}
			if !merged {
				parts = append(parts, p)
			}
		}
		if len(parts) == 0 || sum != rc.amount || len(parts) == 1 && parts[0].line == rc.line && !parts[0].ignored {
			continue
		}
		if len(parts) == 1 {
			var line any
			if !parts[0].ignored {
				line = parts[0].line
			}
			_, err = tx.ExecContext(ctx, `UPDATE import_transactions SET line_id = ?, ignored = ?, rule_id = NULL, by_default = FALSE, by_receipt = TRUE
				WHERE id = ?`, line, parts[0].ignored, rc.id)
			if err != nil {
				return err
			}
			continue
		}
		for _, p := range parts {
			var line any
			if !p.ignored {
				line = p.line
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO import_splits (transaction_id, line_id, ignored, amount) VALUES (?, ?, ?, ?)`,
				rc.id, line, p.ignored, p.amount); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE import_receipt_items SET line_id = ? WHERE id IN (?`+strings.Repeat(", ?", len(p.items)-1)+`)`,
				append([]any{line}, p.items...)...); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE import_transactions SET split = TRUE, line_id = NULL, ignored = FALSE, rule_id = NULL, by_default = FALSE, by_receipt = TRUE
			WHERE id = ?`, rc.id); err != nil {
			return err
		}
	}
	return nil
}
