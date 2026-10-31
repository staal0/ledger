package main

import (
	"net/http/httptest"
	"testing"
)

func TestPager(t *testing.T) {
	for _, tc := range []struct {
		url         string
		total       int
		page, pages int
		size        int
		prev, next  string
	}{
		{"/bank?year=2031", 0, 1, 1, 25, "", ""},
		{"/bank?year=2031", 60, 1, 3, 25, "", "/bank?page=2&year=2031#list"},
		{"/bank?year=2031&page=2", 60, 2, 3, 25, "/bank?year=2031#list", "/bank?page=3&year=2031#list"},
		{"/bank?page=9", 60, 3, 3, 25, "/bank?page=2#list", ""},  // past the end: the last page
		{"/bank?page=-1", 60, 1, 3, 25, "", "/bank?page=2#list"}, // before the start: the first
		{"/bank?size=50&page=2", 120, 2, 3, 50, "/bank?size=50#list", "/bank?page=3&size=50#list"},
		{"/bank?size=7", 60, 1, 3, 25, "", "/bank?page=2&size=7#list"}, // not a size to choose: the default
	} {
		p := newPager(httptest.NewRequest("GET", tc.url, nil), nil, "page", tc.total, "#list")
		if p.Page != tc.page || p.Pages != tc.pages || p.Size != tc.size || p.Prev != tc.prev || p.Next != tc.next {
			t.Errorf("%s of %d: page %d/%d size %d prev %q next %q, want %d/%d %d %q %q",
				tc.url, tc.total, p.Page, p.Pages, p.Size, p.Prev, p.Next, tc.page, tc.pages, tc.size, tc.prev, tc.next)
		}
	}
	if p := newPager(httptest.NewRequest("GET", "/bank?size=100&page=3", nil), nil, "page", 250, ""); p.Offset() != 200 {
		t.Errorf("offset %d on page 3 of 100, want 200", p.Offset())
	}

	// Two lists on one page keep each other's page and size; choosing a size
	// keeps the other parameters and starts again on the first page.
	p := newPager(httptest.NewRequest("GET", "/manage?year=2031&years_page=2&history_page=3&history_size=25", nil), nil, "history_page", 100, "#history")
	if p.Next != "/manage?history_page=4&history_size=25&year=2031&years_page=2#history" {
		t.Errorf("next %q loses the other list's page", p.Next)
	}
	if p.SizeParam != "history_size" || p.Hidden.Encode() != "year=2031&years_page=2" || p.Action != "/manage#history" {
		t.Errorf("size form %q %q %q", p.SizeParam, p.Hidden.Encode(), p.Action)
	}
	if !newPager(httptest.NewRequest("GET", "/bank", nil), nil, "page", 26, "").Shown() || newPager(httptest.NewRequest("GET", "/bank", nil), nil, "page", 25, "").Shown() {
		t.Error("the pager shows for a list that fits on the smallest page, or not for a longer one")
	}
}
