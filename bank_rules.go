package main

import (
	"context"
	"database/sql"
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
	Assigned int // transactions this rule assigned
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
	for _, sec := range sections {
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

func loadBankRules(ctx context.Context, q querier, budget int64) ([]bankRule, error) {
	rows, err := q.QueryContext(ctx, `SELECT r.id, r.pattern, COALESCE(r.section, ''), COALESCE(r.line_name, ''), r.ignored,
			(SELECT COUNT(*) FROM bank_transactions t WHERE t.rule_id = r.id)
		FROM bank_rules r WHERE r.budget_id = ? ORDER BY r.pattern`, budget)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []bankRule
	for rows.Next() {
		var r bankRule
		if err := rows.Scan(&r.ID, &r.Pattern, &r.Section, &r.LineName, &r.Ignored, &r.Assigned); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// applyBankRules assigns the budget's unassigned transactions by its rules and
// returns how many it assigned. A rule's line is looked up by name in the
// transaction's year, so a transaction in a year without that line stays unassigned.
func applyBankRules(ctx context.Context, tx *sql.Tx, budget int64) (int, error) {
	rules, err := loadBankRules(ctx, tx, budget)
	if err != nil || len(rules) == 0 {
		return 0, err
	}
	type key struct {
		year          int
		section, name string
	}
	lines := map[key]int64{}
	rows, err := tx.QueryContext(ctx, `SELECT y.year, l.section, l.name, MIN(l.id) FROM budget_lines l
		JOIN budget_years y ON y.id = l.year_id WHERE y.budget_id = ? GROUP BY y.year, l.section, l.name`, budget)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var k key
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
		id   int64
		text string
		year int
	}
	var todo []open
	rows, err = tx.QueryContext(ctx, `SELECT t.id, t.text, YEAR(t.booked) FROM bank_transactions t
		JOIN bank_accounts a ON a.id = t.account_id WHERE a.budget_id = ? AND t.line_id IS NULL AND NOT t.ignored`, budget)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var o open
		if err := rows.Scan(&o.id, &o.text, &o.year); err != nil {
			return 0, err
		}
		todo = append(todo, o)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	n := 0
	for _, o := range todo {
		r := matchRule(rules, o.text)
		if r == nil {
			continue
		}
		var line any
		if !r.Ignored {
			id, found := lines[key{o.year, r.Section, r.LineName}]
			if !found {
				continue
			}
			line = id
		}
		if _, err := tx.ExecContext(ctx, `UPDATE bank_transactions SET line_id = ?, ignored = ?, rule_id = ? WHERE id = ?`,
			line, r.Ignored, r.ID, o.id); err != nil {
			return 0, err
		}
		n++
	}
	return n, nil
}

// unassignByRules makes the transactions assigned by the given rules
// unassigned again, before the rules change or go.
func unassignByRules(ctx context.Context, tx *sql.Tx, ids []int64) error {
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `UPDATE bank_transactions SET line_id = NULL, ignored = FALSE, rule_id = NULL WHERE rule_id = ?`, id); err != nil {
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
	for _, s := range sections {
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
func (a *App) bankRulesPage(w http.ResponseWriter, r *http.Request) {
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
	rules, err := loadBankRules(ctx, a.db, current.ID)
	if err != nil {
		a.fail(w, err)
		return
	}
	// A rule names its line, so it may point at any line of any year.
	var lines []bankLineChoice
	rows, err := a.db.QueryContext(ctx, `SELECT DISTINCT l.section, l.name FROM budget_lines l
		JOIN budget_years y ON y.id = l.year_id WHERE y.budget_id = ? ORDER BY l.name`, current.ID)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var c bankLineChoice
		if err := rows.Scan(&c.Section, &c.Name); err != nil {
			a.fail(w, err)
			return
		}
		lines = append(lines, c)
	}
	if err := rows.Err(); err != nil {
		a.fail(w, err)
		return
	}
	L := a.locale(r)
	type picker struct {
		Target   string
		L        *Locale
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
	a.render(w, r, "bank_rules", map[string]any{
		"Title":     L.T("nav.bank"),
		"Tabs":      bankTabs(L, current, "rules"),
		"Current":   current,
		"Budgets":   pickable(visible),
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
func (a *App) bankRulesSave(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	b, _, ok, err := a.budgetFor(r)
	if err != nil {
		a.fail(w, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	back := func(q url.Values) string { return bankURL("/bank/rules", b, q) }
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer tx.Rollback()
	rules, err := loadBankRules(ctx, tx, b.ID)
	if err != nil {
		a.fail(w, err)
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
		if pattern != rule.Pattern || section != rule.Section || name != rule.LineName || ignored != rule.Ignored {
			changed = append(changed, rule.ID)
			if _, err := tx.ExecContext(ctx, `UPDATE bank_rules SET pattern = ?, section = NULLIF(?, ''), line_name = NULLIF(?, ''), ignored = ? WHERE id = ?`,
				pattern, section, name, ignored, rule.ID); err != nil {
				a.fail(w, err)
				return
			}
		}
	}
	if err := unassignByRules(ctx, tx, changed); err != nil {
		a.fail(w, err)
		return
	}
	if deleted != 0 {
		if _, err := tx.ExecContext(ctx, `DELETE FROM bank_rules WHERE id = ? AND budget_id = ?`, deleted, b.ID); err != nil {
			a.fail(w, err)
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
		if _, err := tx.ExecContext(ctx, `INSERT INTO bank_rules (budget_id, pattern, section, line_name, ignored) VALUES (?, ?, NULLIF(?, ''), NULLIF(?, ''), ?)`,
			b.ID, pattern, section, name, ignored); err != nil {
			a.fail(w, err)
			return
		}
	}
	if _, err := applyBankRules(ctx, tx, b.ID); err != nil {
		a.fail(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		a.fail(w, err)
		return
	}
	http.Redirect(w, r, back(nil), http.StatusSeeOther)
}
