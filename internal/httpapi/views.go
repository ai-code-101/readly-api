package httpapi

import (
	"net/url"

	"github.com/ai-code-101/readly-api/internal/store"
)

// bookView is the JSON shape returned for a book: the stored fields plus the
// URLs of its assets. URLs are relative to the API origin.
type bookView struct {
	store.Book
	CoverURL string `json:"coverUrl"`
	ThumbURL string `json:"thumbUrl"`
	EPUBURL  string `json:"epubUrl"`
}

func viewBook(b store.Book) bookView {
	v := bookView{Book: b}
	base := "/api/v1/books/" + url.PathEscape(b.Slug)
	if b.CoverVersion != "" {
		v.CoverURL = base + "/cover?v=" + b.CoverVersion
		v.ThumbURL = base + "/cover?size=thumb&v=" + b.CoverVersion
	}
	if b.HasEPUB {
		v.EPUBURL = base + "/epub"
	}
	return v
}

// adminViewBook points asset URLs at the admin endpoints so drafts can be previewed.
func adminViewBook(b store.Book) bookView {
	v := bookView{Book: b}
	base := "/api/v1/admin/books/" + b.ID
	if b.CoverVersion != "" {
		v.CoverURL = base + "/cover?v=" + b.CoverVersion
		v.ThumbURL = base + "/cover?size=thumb&v=" + b.CoverVersion
	}
	if b.HasEPUB {
		v.EPUBURL = base + "/epub"
	}
	return v
}

func viewBooks(bs []store.Book, f func(store.Book) bookView) []bookView {
	out := make([]bookView, len(bs))
	for i, b := range bs {
		out[i] = f(b)
	}
	return out
}

type categoryView struct {
	store.Category
	ImageURL string `json:"imageUrl"`
}

func viewCategory(c store.Category) categoryView {
	v := categoryView{Category: c}
	if c.ImageVersion != "" {
		v.ImageURL = "/api/v1/categories/" + url.PathEscape(c.Slug) + "/image?v=" + c.ImageVersion
	}
	return v
}

type page[T any] struct {
	Items    []T `json:"items"`
	Total    int `json:"total"`
	Page     int `json:"page"`
	PageSize int `json:"pageSize"`
}
