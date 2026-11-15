package bank

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"path"
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

type receiptItem struct {
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

// parseReceipts reads the receipts.json of a receipt export, or the zip it comes in.
func parseReceipts(data []byte) ([]receipt, error) {
	if z, err := zip.NewReader(bytes.NewReader(data), int64(len(data))); err == nil {
		data = nil
		for _, f := range z.File {
			if path.Base(f.Name) != "receipts.json" {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			data, err = io.ReadAll(io.LimitReader(rc, maxReceiptFile))
			rc.Close()
			if err != nil {
				return nil, err
			}
		}
	}
	var raw []struct {
		ReceiptID string `json:"receiptId"`
		Date      string `json:"purchaseDateTimeString"`
		Merchant  struct {
			Name string `json:"name"`
		} `json:"merchant"`
		Price struct {
			Amount float64 `json:"amount"`
		} `json:"price"`
		Lines []struct {
			Name  string `json:"name"`
			Total struct {
				Amount float64 `json:"amount"`
			} `json:"totalPrice"`
		} `json:"receiptLines"`
	}
	if len(data) == 0 || json.Unmarshal(data, &raw) != nil {
		return nil, errNoReceipts
	}
	var out []receipt
	for _, r := range raw {
		at, err := time.Parse(time.RFC3339, r.Date)
		if r.ReceiptID == "" || len(r.ReceiptID) > 64 || err != nil {
			return nil, errNoReceipts
		}
		rc := receipt{ID: r.ReceiptID, Booked: time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, time.UTC),
			Store: cut(r.Merchant.Name, maxBankText), Total: money(r.Price.Amount)}
		if rc.Store == "" {
			rc.Store = r.ReceiptID
		}
		for _, l := range r.Lines {
			rc.Items = append(rc.Items, receiptItem{cut(l.Name, 255), money(l.Total.Amount)})
		}
		out = append(out, rc)
	}
	return out, nil
}

// receiptImport adds the receipts of an export that are not imported yet; the export holds them all.
func (a handlers) receiptImport(w http.ResponseWriter, r *http.Request, acc bankAccount, b app.BudgetRef, receipts []receipt) {
	ctx := r.Context()
	tx, err := a.DB().BeginTx(ctx, nil)
	if err != nil {
		a.Fail(w, err)
		return
	}
	defer tx.Rollback()
	if err := tx.QueryRowContext(ctx, `SELECT id FROM bank_accounts WHERE id = ? FOR UPDATE`, acc.ID).Scan(new(int64)); err != nil {
		a.Fail(w, err)
		return
	}
	have := map[string]bool{}
	rows, err := tx.QueryContext(ctx, `SELECT receipt FROM bank_transactions WHERE account_id = ? AND receipt IS NOT NULL`, acc.ID)
	if err != nil {
		a.Fail(w, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			a.Fail(w, err)
			return
		}
		have[id] = true
	}
	if err := rows.Err(); err != nil {
		a.Fail(w, err)
		return
	}
	var todo []receipt
	for _, rc := range receipts {
		if !have[rc.ID] {
			have[rc.ID] = true // twice in one file counts once
			todo = append(todo, rc)
		}
	}
	if len(todo) > 0 {
		var user any
		if u := app.CurrentUser(r); u != nil && u.ID != 0 {
			user = u.ID
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO bank_imports (account_id, user_id, imported_at, added, skipped) VALUES (?, ?, ?, ?, ?)`,
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
	assigned, err := applyBankRules(ctx, tx, acc.BudgetID)
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		a.Fail(w, err)
		return
	}
	http.Redirect(w, r, bankURL("/bank", b, url.Values{"added": {strconv.Itoa(len(todo))}, "skipped": {strconv.Itoa(len(receipts) - len(todo))},
		"assigned": {strconv.Itoa(assigned)}}), http.StatusSeeOther)
}

func addReceipt(ctx context.Context, tx *sql.Tx, account, importID int64, rc receipt) error {
	res, err := tx.ExecContext(ctx, `INSERT INTO bank_transactions (account_id, import_id, booked, text, amount, seq, receipt)
		SELECT ?, ?, ?, ?, ?, COALESCE(MAX(seq), 0) + 1, ? FROM bank_transactions WHERE account_id = ? AND booked = ? AND amount = ? AND text = ?`,
		account, importID, rc.Booked, rc.Store, -rc.Total, rc.ID, account, rc.Booked, -rc.Total, rc.Store)
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	for _, it := range rc.Items {
		if _, err := tx.ExecContext(ctx, `INSERT INTO bank_receipt_items (transaction_id, name, amount) VALUES (?, ?, ?)`, id, it.Name, it.Amount); err != nil {
			return err
		}
	}
	return nil
}

// receiptItems are a transaction's receipt items, if it is a receipt.
func receiptItems(ctx context.Context, q app.Querier, transaction int64) ([]receiptItem, error) {
	rows, err := q.QueryContext(ctx, `SELECT name, amount FROM bank_receipt_items WHERE transaction_id = ? ORDER BY id`, transaction)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []receiptItem
	for rows.Next() {
		var it receiptItem
		if err := rows.Scan(&it.Name, &it.Amount); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// resetByReceipt undoes what receipts did, so applyReceipts can redo it; it returns those transactions.
func resetByReceipt(ctx context.Context, tx *sql.Tx, budget int64) (map[int64]bool, error) {
	ids := map[int64]bool{}
	rows, err := tx.QueryContext(ctx, `SELECT t.id FROM bank_transactions t JOIN bank_accounts a ON a.id = t.account_id
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
	if _, err := tx.ExecContext(ctx, `DELETE s FROM bank_splits s JOIN bank_transactions t ON t.id = s.transaction_id
		JOIN bank_accounts a ON a.id = t.account_id WHERE a.budget_id = ? AND t.by_receipt`, budget); err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE bank_transactions t JOIN bank_accounts a ON a.id = t.account_id
		SET t.split = FALSE, t.line_id = NULL, t.ignored = FALSE, t.rule_id = NULL, t.by_default = FALSE, t.by_receipt = FALSE
		WHERE a.budget_id = ? AND t.by_receipt`, budget)
	return ids, err
}

// applyReceipts lets receipts take over from rules and default lines: a bank
// transaction a receipt covers (same amount, a few days later at most) is
// ignored, and a receipt is split by rules for its items, the rest staying on
// the receipt's own line.
func applyReceipts(ctx context.Context, tx *sql.Tx, budget int64, rules []bankRule, lines map[lineKey]int64) error {
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
		FROM bank_transactions t JOIN bank_accounts a ON a.id = t.account_id
		WHERE a.budget_id = ? AND NOT t.split ORDER BY t.booked, t.id`, budget)
	if err != nil {
		return err
	}
	defer rows.Close()
	var receipts []row
	bank := map[app.Money][]*row{}
	for rows.Next() {
		var o row
		if err := rows.Scan(&o.id, &o.booked, &o.amount, &o.line, &o.ignored, &o.receipt, &o.free); err != nil {
			return err
		}
		if o.receipt {
			receipts = append(receipts, o)
		} else if o.free {
			bank[o.amount] = append(bank[o.amount], &o)
		}
	}
	if err := rows.Err(); err != nil || len(receipts) == 0 {
		return err
	}

	// The bank books a purchase on the day or a few days later.
	for _, rc := range receipts {
		if rc.ignored {
			continue
		}
		var best *row
		for _, t := range bank[rc.amount] {
			days := t.booked.Sub(rc.booked).Hours() / 24
			if days < -1 || days > 5 || t.receipt { // receipt marks one taken
				continue
			}
			if best == nil || math.Abs(days) < math.Abs(best.booked.Sub(rc.booked).Hours()/24) {
				best = t
			}
		}
		if best != nil {
			best.receipt = true
			if _, err := tx.ExecContext(ctx, `UPDATE bank_transactions SET line_id = NULL, ignored = TRUE, rule_id = NULL, by_default = FALSE, by_receipt = TRUE
				WHERE id = ?`, best.id); err != nil {
				return err
			}
		}
	}

	type part struct {
		line    int64
		ignored bool
		amount  app.Money
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
			p := part{line: rc.line, amount: -it.Amount}
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
			_, err = tx.ExecContext(ctx, `UPDATE bank_transactions SET line_id = ?, ignored = ?, rule_id = NULL, by_default = FALSE, by_receipt = TRUE
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
			if _, err := tx.ExecContext(ctx, `INSERT INTO bank_splits (transaction_id, line_id, ignored, amount) VALUES (?, ?, ?, ?)`,
				rc.id, line, p.ignored, p.amount); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE bank_transactions SET split = TRUE, line_id = NULL, ignored = FALSE, rule_id = NULL, by_default = FALSE, by_receipt = TRUE
			WHERE id = ?`, rc.id); err != nil {
			return err
		}
	}
	return nil
}
