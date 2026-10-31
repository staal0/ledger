package app

import (
	"context"
	"net/http/httptest"
	"regexp"
	"testing"
)

func testLocales(t *testing.T) map[string]*Locale {
	t.Helper()
	locales, err := loadLocales("")
	if err != nil {
		t.Fatal(err)
	}
	return locales
}

func TestTranslate(t *testing.T) {
	da := testLocales(t)["da"]
	if got := da.T("nav.settings"); got != "Indstillinger" {
		t.Errorf("da nav.settings = %q", got)
	}
	if got := da.T("budget.title", 2026); got != "Budget 2026" {
		t.Errorf("with argument: %q", got)
	}
	da.msgs = map[string]string{} // nothing translated: falls back to English
	if got := da.T("nav.settings"); got != "Settings" {
		t.Errorf("fallback to English = %q", got)
	}
	if got := da.T("no.such.key"); got != "no.such.key" {
		t.Errorf("unknown key = %q, want the key itself", got)
	}
}

func TestLocaleFiles(t *testing.T) {
	// Every Danish text must exist in English, the fallback for all languages.
	locales := testLocales(t)
	for key := range locales["da"].msgs {
		if _, ok := locales["en"].msgs[key]; !ok {
			t.Errorf("%q is in da.json but missing from en.json", key)
		}
	}
	for key := range locales["en"].msgs {
		if _, ok := locales["da"].msgs[key]; !ok {
			t.Errorf("%q is in en.json but missing from da.json", key)
		}
	}
}

func TestLanguageAndNumberFormat(t *testing.T) {
	a := &App{locales: testLocales(t)}
	tests := []struct {
		acceptLanguage string
		user           *User
		wantLang       string
		wantDecimal    string
	}{
		{"da-DK,da;q=0.9,en;q=0.8", nil, "da", ","},
		{"en-US,en;q=0.9", nil, "en", "."},
		{"de-DE,de;q=0.9,da;q=0.5", nil, "da", ","}, // first supported one wins
		{"fr-FR", nil, "en", "."},                   // nothing supported: English
		{"", nil, "en", "."},
		{"da", &User{Lang: "en"}, "en", "."},                        // the user's choice beats the browser
		{"da", &User{Lang: "en", NumberFormat: "comma"}, "en", ","}, // English words, Danish money
		{"en", &User{NumberFormat: "comma"}, "en", ","},
		{"da", &User{NumberFormat: "point"}, "da", "."},
		{"da", &User{Lang: "xx", NumberFormat: "xx"}, "da", ","}, // unknown values are ignored
	}
	for _, tt := range tests {
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("Accept-Language", tt.acceptLanguage)
		if tt.user != nil {
			r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, tt.user))
		}
		l := a.Locale(r)
		if l.Code != tt.wantLang || l.Decimal != tt.wantDecimal {
			t.Errorf("Accept-Language %q, user %+v: got %s with %q, want %s with %q",
				tt.acceptLanguage, tt.user, l.Code, l.Decimal, tt.wantLang, tt.wantDecimal)
		}
	}
	// Choosing a number format must not change the shared locale.
	if a.locales["en"].Decimal != "." {
		t.Error("the English locale was modified")
	}
}

// The browser checks amounts with InputPattern before they are sent; it must
// accept exactly what ParseMoney accepts, or a field looks valid but the save fails.
func TestInputPatternMatchesParser(t *testing.T) {
	inputs := []string{
		"", "0", "12", "12500", "-5", "-", "12.500", "1.234.567,89", "12,5", ",5",
		"12.5", "1.23.456", "1234.567", "12,345", "1,234.56", "1,234,567.8",
		".5", "abc", "1 000", "--5", "5-",
	}
	for _, f := range numberFormats {
		l := &Locale{Decimal: f[0], Thousands: f[1]}
		re := regexp.MustCompile(`^(?:` + l.InputPattern() + `)$`)
		for _, in := range inputs {
			_, err := l.ParseMoney(in)
			if browser, server := re.MatchString(in), err == nil; browser != server {
				t.Errorf("decimal %q: %q is valid in the browser: %v, on the server: %v", f[0], in, browser, server)
			}
		}
	}
}

func TestDelta(t *testing.T) {
	locales := testLocales(t)
	en, da := locales["en"], locales["da"]
	tests := []struct {
		l         *Locale
		cur, prev Money
		upGood    int
		text, cls string
	}{
		{en, 110, 100, 1, "▲ +10.0%", "good"},
		{en, 110, 100, -1, "▲ +10.0%", "bad"}, // more expenses
		{en, 90, 100, -1, "▼ -10.0%", "good"},
		{en, 110, 100, 0, "▲ +10.0%", ""},
		{en, 100, 100, 1, "+0.0%", ""},
		{en, 100, 0, 1, "–", ""}, // nothing to compare with
		{da, 1234, 1000, 1, "▲ +23,4 %", "good"},
		{en, -50, -100, 1, "▲ +50.0%", "good"}, // from a deficit of 100 to one of 50
	}
	for _, tt := range tests {
		d := tt.l.delta(tt.cur, tt.prev, tt.upGood)
		if d.Text != tt.text || d.Class != tt.cls {
			t.Errorf("%s delta(%d, %d, %d) = %q %q, want %q %q", tt.l.Code, tt.cur, tt.prev, tt.upGood, d.Text, d.Class, tt.text, tt.cls)
		}
	}
}
