package main

import (
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// pageSizes are the page sizes to choose from; the first list shown has
// pageSizeDefault items a page.
var pageSizes = []int{25, 50, 100, 200}

const pageSizeDefault = 25

// pager is one page of a long list. The page is the request's ?param= (from
// 1) and the size its ?…size=, named after param: "page" goes with "size",
// "history_page" with "history_size". Its links keep the request's other
// parameters, so the budget, year and filters stay as they are.
type pager struct {
	L                  *Locale
	Page, Pages, Total int
	Size               int
	Prev, Next         string // URLs, "" on the first and last page

	// For choosing the size: a form to Action with the request's other
	// parameters as Hidden fields, which starts again on the first page.
	Sizes     []int
	SizeParam string
	Action    string
	Hidden    url.Values
}

// newPager pages total items, on the page and at the size the request asks
// for, kept within the pages there are. anchor, such as "#history", is added
// to the links so the browser returns to the list.
func newPager(r *http.Request, L *Locale, param string, total int, anchor string) pager {
	sizeParam := strings.TrimSuffix(param, "page") + "size"
	size, _ := strconv.Atoi(r.URL.Query().Get(sizeParam))
	if !slices.Contains(pageSizes, size) {
		size = pageSizeDefault
	}
	p := pager{L: L, Size: size, Total: total, Pages: max(1, (total+size-1)/size),
		Sizes: pageSizes, SizeParam: sizeParam, Action: r.URL.Path + anchor}
	p.Page, _ = strconv.Atoi(r.URL.Query().Get(param))
	p.Page = min(max(p.Page, 1), p.Pages)
	link := func(page int) string {
		q := r.URL.Query()
		if page == 1 {
			q.Del(param)
		} else {
			q.Set(param, strconv.Itoa(page))
		}
		u := r.URL.Path
		if len(q) > 0 {
			u += "?" + q.Encode()
		}
		return u + anchor
	}
	if p.Page > 1 {
		p.Prev = link(p.Page - 1)
	}
	if p.Page < p.Pages {
		p.Next = link(p.Page + 1)
	}
	p.Hidden = r.URL.Query()
	p.Hidden.Del(param)
	p.Hidden.Del(sizeParam)
	return p
}

// Offset is the number of items before the page, for OFFSET.
func (p pager) Offset() int { return (p.Page - 1) * p.Size }

// Shown reports whether the list is long enough for the pager: more than the
// smallest page size, so that a size can be chosen.
func (p pager) Shown() bool { return p.Total > p.Sizes[0] }
