package app

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"
)

// Locale is one language, loaded from <code>.json. Keys starting with "_"
// hold metadata: _name, _decimal and _thousands.
type Locale struct {
	Code      string
	Name      string
	Decimal   string
	Thousands string
	msgs      map[string]string
	fallback  *Locale
}

// loadLocales reads the built-in languages and then dir (if set), where files
// can add new languages or override single keys of built-in ones.
func loadLocales(dir string) (map[string]*Locale, error) {
	locales := map[string]*Locale{}
	load := func(fsys fs.FS) error {
		files, err := fs.Glob(fsys, "*.json")
		if err != nil {
			return err
		}
		for _, f := range files {
			b, err := fs.ReadFile(fsys, f)
			if err != nil {
				return err
			}
			var msgs map[string]string
			if err := json.Unmarshal(b, &msgs); err != nil {
				return fmt.Errorf("%s: %w", f, err)
			}
			code := strings.TrimSuffix(f, ".json")
			if locales[code] == nil {
				locales[code] = &Locale{Code: code, msgs: map[string]string{}}
			}
			maps.Copy(locales[code].msgs, msgs)
		}
		return nil
	}
	builtin, _ := fs.Sub(embedded, "locales")
	if err := load(builtin); err != nil {
		return nil, err
	}
	for _, p := range plugins {
		if fsys := p.Locales(); fsys != nil {
			if err := load(fsys); err != nil {
				return nil, fmt.Errorf("plugin %s: %w", p.ID(), err)
			}
		}
	}
	// The directory comes last, so its texts win over the built-in ones.
	if dir != "" {
		if err := load(os.DirFS(dir)); err != nil {
			return nil, err
		}
	}
	en := locales["en"]
	for _, l := range locales {
		l.Name = l.msg("_name", l.Code)
		l.Decimal = l.msg("_decimal", ".")
		l.Thousands = l.msg("_thousands", "")
		if l != en {
			l.fallback = en
		}
	}
	return locales, nil
}

func (l *Locale) msg(key, def string) string {
	if s, ok := l.msgs[key]; ok {
		return s
	}
	return def
}

// T translates key, falling back to English and then to the key itself.
func (l *Locale) T(key string, args ...any) string {
	for x := l; x != nil; x = x.fallback {
		if s, ok := x.msgs[key]; ok {
			if len(args) > 0 {
				return fmt.Sprintf(s, args...)
			}
			return s
		}
	}
	return key
}

func (l *Locale) Money(m Money) string { return m.Format(l.Decimal, l.Thousands) }

// Input formats m for an input field: like Money, but empty for zero.
func (l *Locale) Input(m Money) string {
	if m == 0 {
		return ""
	}
	return l.Money(m)
}

func (l *Locale) ParseMoney(s string) (Money, error) { return ParseMoney(s, l.Decimal, l.Thousands) }

// InputPattern is the HTML pattern matching what ParseMoney accepts: digits
// either plain or in groups of three, then up to two decimals.
func (l *Locale) InputPattern() string {
	whole := `\d*`
	if l.Thousands != "" {
		whole = `(\d{1,3}(` + regexp.QuoteMeta(l.Thousands) + `\d{3})*|\d*)`
	}
	return `-?` + whole + `(` + regexp.QuoteMeta(l.Decimal) + `\d{1,2})?`
}

func (a *App) sortedLocales() []*Locale {
	return slices.SortedFunc(maps.Values(a.locales), func(x, y *Locale) int { return strings.Compare(x.Name, y.Name) })
}

// numberFormats are the selectable money formats as {decimal, thousands}.
var numberFormats = map[string][2]string{
	"comma": {",", "."}, // 1.234,56
	"point": {".", ","}, // 1,234.56
}

// Locale is the request's language, with the user's number format applied.
func (a *App) Locale(r *http.Request) *Locale {
	l := a.language(r)
	if u := CurrentUser(r); u != nil {
		if f, ok := numberFormats[u.NumberFormat]; ok {
			c := *l
			c.Decimal, c.Thousands = f[0], f[1]
			return &c
		}
	}
	return l
}

// language picks the user's chosen language, else the first match from the
// browser's Accept-Language (in its order of preference), else English.
func (a *App) language(r *http.Request) *Locale {
	if u := CurrentUser(r); u != nil {
		if l, ok := a.locales[u.Lang]; ok {
			return l
		}
	}
	for _, tag := range strings.Split(r.Header.Get("Accept-Language"), ",") {
		tag, _, _ = strings.Cut(strings.ToLower(strings.TrimSpace(tag)), ";")
		primary, _, _ := strings.Cut(tag, "-")
		for _, code := range []string{tag, primary} {
			if l, ok := a.locales[code]; ok {
				return l
			}
		}
	}
	return a.locales["en"]
}
