package bank

import (
	"context"
	"database/sql"
	"github.com/staal0/ledger/internal/app"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// bankRule assigns the transactions whose text contains Pattern, in any case,
// to the line named Section and LineName in the transaction's year, or ignores
// them. A * in Pattern stands for any text. Rules only assign transactions that are unassigned; those they assign
// keep the rule's id, so they follow when the rule is changed or deleted.
type bankRule struct {
	ID       int64
	Pattern  string
	Section  string
	LineName string
	Ignored  bool
	Display  string // shown instead of the bank's text, if set
	Assigned int    // transactions this rule assigned
}

// Target is the rule's choice as the rule form sends it.
func (r bankRule) Target() string {
	if r.Ignored {
		return "ignore"
	}
	return r.Section + ":" + r.LineName
}

// parseRuleTarget reads a target from the rule form: "ignore" or "section:name".
func parseRuleTarget(s string) (section, name string, ignored, ok bool) {
	if s == "ignore" {
		return "", "", true, true
	}
	section, name, found := strings.Cut(s, ":")
	if !found || name == "" {
		return "", "", false, false
	}
	for _, sec := range app.Sections {
		if sec == section {
			return section, name, false, true
		}
	}
	return "", "", false, false
}

// dateInText matches dates in a transaction text, such as 12.03, 12-03-2026
// or 12/03/26, which differ from month to month.
var dateInText = regexp.MustCompile(`\b\d{1,2}[./-]\d{1,2}(?:[./-]\d{2,4})?\b`)

// rulePattern is the pattern a rule learns from a transaction's text: the text
// without dates. Numbers are kept, as an account or loan number names the payee.
func rulePattern(text string) string {
	p := strings.Join(strings.Fields(dateInText.ReplaceAllString(text, " ")), " ")
	if p == "" {
		p = text
	}
	return p
}

// patternMatches reports whether text contains pattern, in any case, where a
// * in pattern stands for any text: the parts between the stars appear in text
// in that order.
func patternMatches(text, pattern string) bool {
	text = strings.ToLower(text)
	for _, part := range strings.Split(strings.ToLower(pattern), "*") {
		i := strings.Index(text, part)
		if i < 0 {
			return false
		}
		text = text[i+len(part):]
	}
	return true
}

// specificity is how much of a text a pattern fixes: its length without stars.
func specificity(pattern string) int {
	return utf8.RuneCountInString(strings.ReplaceAll(pattern, "*", ""))
}

// matchRule is the rule for text: of those whose pattern matches, the most
// specific, so a rule for one store wins over one for the chain.
func matchRule(rules []bankRule, text string) *bankRule {
	var best *bankRule
	for i, r := range rules {
		if patternMatches(text, r.Pattern) && (best == nil || specificity(r.Pattern) > specificity(best.Pattern)) {
			best = &rules[i]
		}
	}
	return best
}

func loadBankRules(ctx context.Context, q app.Querier, budget int64) ([]bankRule, error) {
	rows, err := q.QueryContext(ctx, `SELECT r.id, r.pattern, COALESCE(r.section, ''), COALESCE(r.line_name, ''), r.ignored,
			COALESCE(r.display_name, ''), (SELECT COUNT(*) FROM bank_transactions t WHERE t.rule_id = r.id)
		FROM bank_rules r WHERE r.budget_id = ? ORDER BY r.pattern`, budget)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []bankRule
	for rows.Next() {
		var r bankRule
		if err := rows.Scan(&r.ID, &r.Pattern, &r.Section, &r.LineName, &r.Ignored, &r.Display, &r.Assigned); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// applyBankRules assigns the budget's unassigned transactions by its rules,
// then by their account's default line, then lets receipts take over, and
// returns how many it assigned.
// Lines are looked up by name in the transaction's year. Transactions the
// default assigned are checked again, so they follow rule and default changes.
func applyBankRules(ctx context.Context, tx *sql.Tx, budget int64) (int, error) {
	redo, err := resetByReceipt(ctx, tx, budget)
	if err != nil {
		return 0, err
	}
	rules, err := loadBankRules(ctx, tx, budget)
	if err != nil {
		return 0, err
	}
	lines := map[lineKey]int64{}
	rows, err := tx.QueryContext(ctx, `SELECT y.year, l.section, l.name, MIN(l.id) FROM budget_lines l
		JOIN budget_years y ON y.id = l.year_id WHERE y.budget_id = ? GROUP BY y.year, l.section, l.name`, budget)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var k lineKey
		var id int64
		if err := rows.Scan(&k.year, &k.section, &k.name, &id); err != nil {
			return 0, err
		}
		lines[k] = id
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	type open struct {
		id            int64
		text          string
		year          int
		byDefault     bool
		section, name string // the account's default line, if any
	}
	var todo []open
	rows, err = tx.QueryContext(ctx, `SELECT t.id, t.text, YEAR(t.booked), t.by_default, COALESCE(a.default_section, ''), COALESCE(a.default_line_name, '')
		FROM bank_transactions t JOIN bank_accounts a ON a.id = t.account_id
		WHERE a.budget_id = ? AND (t.by_default OR t.line_id IS NULL AND NOT t.ignored AND NOT t.split)`, budget)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var o open
		if err := rows.Scan(&o.id, &o.text, &o.year, &o.byDefault, &o.section, &o.name); err != nil {
			return 0, err
		}
		todo = append(todo, o)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	n := 0
	for _, o := range todo {
		var line, rule any
		ignored, byDefault := false, false
		if r := matchRule(rules, o.text); r != nil {
			if id, found := lines[lineKey{o.year, r.Section, r.LineName}]; found || r.Ignored {
				rule, ignored = r.ID, r.Ignored
				if !r.Ignored {
					line = id
				}
			}
		}
		if rule == nil {
			if id, found := lines[lineKey{o.year, o.section, o.name}]; found {
				line, byDefault = id, true
			}
		}
		if line == nil && !ignored && !o.byDefault {
			continue // nothing fits, and nothing to undo
		}
		if _, err := tx.ExecContext(ctx, `UPDATE bank_transactions SET line_id = ?, ignored = ?, rule_id = ?, by_default = ? WHERE id = ?`,
			line, ignored, rule, byDefault, o.id); err != nil {
			return 0, err
		}
		if !o.byDefault && !redo[o.id] && (line != nil || ignored) {
			n++
		}
	}
	return n, applyReceipts(ctx, tx, budget, rules, lines)
}

// lineKey finds a line by name in a year.
type lineKey struct {
	year          int
	section, name string
}

// unassignByRules makes the transactions assigned by the given rules
// unassigned again, before the rules change or go.
func unassignByRules(ctx context.Context, tx *sql.Tx, ids []int64) error {
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `UPDATE bank_transactions SET line_id = NULL, ignored = FALSE, rule_id = NULL, by_default = FALSE WHERE rule_id = ?`, id); err != nil {
			return err
		}
	}
	return nil
}

// bankLineChoice is a line a rule or a transaction can be assigned to.
type bankLineChoice struct {
	ID      int64 // in one year; 0 when listed by name across years
	Section string
	Name    string
}

// Target is the choice as the rule form sends it.
func (c bankLineChoice) Target() string { return c.Section + ":" + c.Name }

// bankSection is a section's lines for a picker.
type bankSection struct {
	Key   string
	Lines []bankLineChoice
}

// groupLines puts lines in sections, in the budget's order of sections.
func groupLines(lines []bankLineChoice) []bankSection {
	var out []bankSection
	for _, s := range app.Sections {
		g := bankSection{Key: s}
		for _, l := range lines {
			if l.Section == s {
				g.Lines = append(g.Lines, l)
			}
		}
		if len(g.Lines) > 0 {
			out = append(out, g)
		}
	}
	return out
}

// bankRulesPage lists the chosen budget's rules, editable for editors.
func (a handlers) bankRulesPage(w http.ResponseWriter, r *http.Request) {
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
	rules, err := loadBankRules(ctx, a.DB(), current.ID)
	if err != nil {
		a.Fail(w, err)
		return
	}
	// A rule names its line, so it may point at any line of any year.
	lines, err := lineNames(ctx, a.DB(), current.ID)
	if err != nil {
		a.Fail(w, err)
		return
	}
	L := a.Locale(r)
	type picker struct {
		Target   string
		L        *app.Locale
		Sections []bankSection
	}
	grouped := groupLines(lines)
	known := map[string]bool{}
	for _, l := range lines {
		known[l.Target()] = true
	}
	pickers := map[int64]picker{}
	for _, rule := range rules {
		pickers[rule.ID] = picker{rule.Target(), L, grouped}
	}
	test := strings.Join(strings.Fields(r.FormValue("test")), " ")
	var matches []bankRuleMatch
	if specificity(test) > 0 {
		if matches, err = a.ruleMatches(r, current.ID, rules, test); err != nil {
			a.Fail(w, err)
			return
		}
	}
	a.Render(w, r, "bank_rules", map[string]any{
		"Test":      test,
		"Matches":   matches,
		"Title":     L.T("nav.bank"),
		"Tabs":      bankTabs(L, current, "rules"),
		"Current":   current,
		"Budgets":   app.Pickable(visible),
		"Rules":     rules,
		"Known":     known,
		"Pickers":   pickers,
		"NewPicker": picker{"", L, grouped},
		"Error":     r.FormValue("error"),
	})
}

// bankRulesSave stores the rule form: every rule's pattern and target, a new
// rule when its pattern is filled in, and deletes the rule of a delete button.
// The transactions a changed rule assigned are assigned again by the rules.
func (a handlers) bankRulesSave(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	b, _, ok, err := a.BudgetFor(r)
	if err != nil {
		a.Fail(w, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	back := func(q url.Values) string { return bankURL("/bank/rules", b, q) }
	tx, err := a.DB().BeginTx(ctx, nil)
	if err != nil {
		a.Fail(w, err)
		return
	}
	defer tx.Rollback()
	rules, err := loadBankRules(ctx, tx, b.ID)
	if err != nil {
		a.Fail(w, err)
		return
	}

	deleted, _ := strconv.ParseInt(strings.TrimPrefix(r.FormValue("action"), "delete:"), 10, 64)
	seen := map[string]bool{}
	var changed []int64
	for _, rule := range rules {
		id := strconv.FormatInt(rule.ID, 10)
		if rule.ID == deleted {
			changed = append(changed, rule.ID)
			continue
		}
		pattern := strings.Join(strings.Fields(r.FormValue("pattern_"+id)), " ")
		section, name, ignored, valid := parseRuleTarget(r.FormValue("target_" + id))
		if !r.Form.Has("pattern_" + id) {
			pattern, section, name, ignored, valid = rule.Pattern, rule.Section, rule.LineName, rule.Ignored, true
		}
		if specificity(pattern) == 0 || !valid || utf8.RuneCountInString(pattern) > maxBankText {
			http.Error(w, "invalid rule", http.StatusBadRequest)
			return
		}
		if seen[strings.ToLower(pattern)] {
			http.Redirect(w, r, back(url.Values{"error": {"duplicate"}}), http.StatusSeeOther)
			return
		}
		seen[strings.ToLower(pattern)] = true
		// The display name only changes how transactions are shown.
		if display := strings.Join(strings.Fields(r.FormValue("display_"+id)), " "); r.Form.Has("display_"+id) && display != rule.Display {
			if _, err := tx.ExecContext(ctx, `UPDATE bank_rules SET display_name = NULLIF(?, '') WHERE id = ?`, display, rule.ID); err != nil {
				a.Fail(w, err)
				return
			}
		}
		if pattern != rule.Pattern || section != rule.Section || name != rule.LineName || ignored != rule.Ignored {
			changed = append(changed, rule.ID)
			if _, err := tx.ExecContext(ctx, `UPDATE bank_rules SET pattern = ?, section = NULLIF(?, ''), line_name = NULLIF(?, ''), ignored = ? WHERE id = ?`,
				pattern, section, name, ignored, rule.ID); err != nil {
				a.Fail(w, err)
				return
			}
		}
	}
	if err := unassignByRules(ctx, tx, changed); err != nil {
		a.Fail(w, err)
		return
	}
	if deleted != 0 {
		if _, err := tx.ExecContext(ctx, `DELETE FROM bank_rules WHERE id = ? AND budget_id = ?`, deleted, b.ID); err != nil {
			a.Fail(w, err)
			return
		}
	}
	if pattern := strings.Join(strings.Fields(r.FormValue("new_pattern")), " "); pattern != "" {
		if specificity(pattern) == 0 {
			http.Redirect(w, r, back(url.Values{"error": {"empty"}}), http.StatusSeeOther)
			return
		}
		if r.FormValue("new_target") == "" {
			http.Redirect(w, r, back(url.Values{"error": {"target"}}), http.StatusSeeOther)
			return
		}
		section, name, ignored, valid := parseRuleTarget(r.FormValue("new_target"))
		if !valid || utf8.RuneCountInString(pattern) > maxBankText {
			http.Error(w, "invalid rule", http.StatusBadRequest)
			return
		}
		if seen[strings.ToLower(pattern)] {
			http.Redirect(w, r, back(url.Values{"error": {"duplicate"}}), http.StatusSeeOther)
			return
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO bank_rules (budget_id, pattern, section, line_name, ignored, display_name)
			VALUES (?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, NULLIF(?, ''))`,
			b.ID, pattern, section, name, ignored, strings.Join(strings.Fields(r.FormValue("new_display")), " ")); err != nil {
			a.Fail(w, err)
			return
		}
	}
	if _, err := applyBankRules(ctx, tx, b.ID); err != nil {
		a.Fail(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		a.Fail(w, err)
		return
	}
	http.Redirect(w, r, back(nil), http.StatusSeeOther)
}

// bankRuleMatch is a transaction a tested text matches. Takes is false when a
// choice by hand or a more specific rule keeps it from the text.
type bankRuleMatch struct {
	bankTransaction
	State string // "unassigned", "hand", "split", "receipt", "default" or "rule"
	Line  string // the line or rule that has it now
	Takes bool
}

// ruleMatches lists the budget's newest transactions that test matches.
func (a handlers) ruleMatches(r *http.Request, budget int64, rules []bankRule, test string) ([]bankRuleMatch, error) {
	rows, err := a.DB().QueryContext(r.Context(), `SELECT t.id, t.booked, t.text, t.amount, t.ignored, t.split, t.by_default, t.by_receipt, COALESCE(l.name, ''), COALESCE(ru.pattern, '')
		FROM bank_transactions t JOIN bank_accounts a ON a.id = t.account_id
		LEFT JOIN budget_lines l ON l.id = t.line_id LEFT JOIN bank_rules ru ON ru.id = t.rule_id
		WHERE a.budget_id = ? ORDER BY t.booked DESC, t.id DESC`, budget)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []bankRuleMatch
	for rows.Next() && len(out) < 50 {
		var m bankRuleMatch
		var rule string
		var split, byDefault, byReceipt bool
		if err := rows.Scan(&m.ID, &m.Booked, &m.Text, &m.Amount, &m.Ignored, &split, &byDefault, &byReceipt, &m.Line, &rule); err != nil {
			return nil, err
		}
		if !patternMatches(m.Text, test) {
			continue
		}
		switch {
		case byReceipt:
			m.State = "receipt"
		case split:
			m.State = "split"
		case byDefault:
			m.State = "default"
		case rule != "":
			m.State, m.Line = "rule", rule
		case m.Line != "" || m.Ignored:
			m.State = "hand"
		default:
			m.State = "unassigned"
		}
		// The text takes what nothing assigned by hand, unless a more specific rule fits.
		best := matchRule(rules, m.Text)
		m.Takes = (m.State == "unassigned" || m.State == "default" || m.State == "rule") && (best == nil || specificity(test) >= specificity(best.Pattern))
		out = append(out, m)
	}
	return out, rows.Err()
}

// displayName is the name a transaction is shown with: its rule's, else its text.
func displayName(rules []bankRule, text string) string {
	if r := matchRule(rules, text); r != nil && r.Display != "" {
		return r.Display
	}
	return text
}

// lineNames lists the budget's lines by section and name, across its years.
func lineNames(ctx context.Context, q app.Querier, budget int64) ([]bankLineChoice, error) {
	rows, err := q.QueryContext(ctx, `SELECT DISTINCT l.section, l.name FROM budget_lines l
		JOIN budget_years y ON y.id = l.year_id WHERE y.budget_id = ? ORDER BY l.name`, budget)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []bankLineChoice
	for rows.Next() {
		var c bankLineChoice
		if err := rows.Scan(&c.Section, &c.Name); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
