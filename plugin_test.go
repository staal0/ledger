package main

import (
	"net/url"
	"strings"
	"testing"
)

func TestPluginSwitch(t *testing.T) {
	a := testApp(t, false)
	menu := func() bool {
		return strings.Contains(request(a, "GET", "/stats", nil, "").Body.String(), `href="/bank"`)
	}

	// Off until turned on: no menu entry and no pages.
	mustStatus(t, request(a, "GET", "/bank", nil, ""), 404)
	mustStatus(t, request(a, "POST", "/bank/accounts", url.Values{"name": {"Budget account"}, "format": {"laanspar"}}, ""), 404)
	if menu() {
		t.Error("the menu shows a plugin that is off")
	}
	if page := request(a, "GET", "/manage", nil, "").Body.String(); !strings.Contains(page, `name="on" value="bank"`) {
		t.Error("the manage page has no switch for the plugin")
	}

	mustStatus(t, request(a, "POST", "/manage/plugins", url.Values{"on": {"bank"}}, ""), 303)
	mustStatus(t, request(a, "GET", "/bank", nil, ""), 200)
	if !menu() {
		t.Error("the menu lacks a plugin that is on")
	}
	mustStatus(t, request(a, "POST", "/bank/accounts", url.Values{"name": {"Budget account"}, "format": {"laanspar"}}, ""), 303)

	// Off again: hidden, and its data is kept.
	mustStatus(t, request(a, "POST", "/manage/plugins", url.Values{}, ""), 303)
	mustStatus(t, request(a, "GET", "/bank", nil, ""), 404)
	if n := queryInt(t, "SELECT COUNT(*) FROM bank_accounts"); n != 1 {
		t.Errorf("%d accounts after turning the plugin off, want 1 kept", n)
	}
}
