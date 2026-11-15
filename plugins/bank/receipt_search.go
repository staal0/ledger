package bank

import (
	"context"
	"strings"
	"time"

	"github.com/staal0/ledger/internal/app"
)

const maxFound = 200

// foundItem is a receipt item a search found, with the month it opens.
type foundItem struct {
	Booked time.Time
	Store  string
	Name   string
	Amount app.Money
	URL    string
}

// likePattern is a search as SQL LIKE: contains, with * for any text.
func likePattern(q string) string {
	q = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q)
	return "%" + strings.ReplaceAll(q, "*", "%") + "%"
}

// searchItems finds the items named like q on the lines the query lines
// selects, newest first; more is true when there are more than it returns.
func searchItems(ctx context.Context, q app.Querier, lines string, args []any, search string) (found []foundItem, more bool, err error) {
	rows, err := q.QueryContext(ctx, `SELECT t.booked, t.text, i.name, i.amount
		FROM bank_receipt_items i JOIN bank_transactions t ON t.id = i.transaction_id
		WHERE COALESCE(i.line_id, t.line_id) IN (`+lines+`) AND i.name LIKE ?
		ORDER BY t.booked DESC, i.id LIMIT ?`, append(append([]any{}, args...), likePattern(search), maxFound+1)...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var f foundItem
		if err := rows.Scan(&f.Booked, &f.Store, &f.Name, &f.Amount); err != nil {
			return nil, false, err
		}
		found = append(found, f)
	}
	if len(found) > maxFound {
		return found[:maxFound], true, rows.Err()
	}
	return found, false, rows.Err()
}
