// Package epub validates EPUB uploads and extracts the metadata the admin
// form is pre-filled with: title, author, language, description, cover image
// and an estimated page count.
package epub

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"regexp"
	"strings"
	"unicode"
)

// WordsPerPage is the divisor used to estimate a printed page count.
const WordsPerPage = 275

// maxEntryBytes caps how much of any single entry we decompress, as a guard
// against zip bombs.
const maxEntryBytes = 50 << 20

var ErrInvalid = errors.New("not a valid EPUB file")

type Metadata struct {
	Title       string `json:"title"`
	Author      string `json:"author"`
	Language    string `json:"language"`
	Description string `json:"description"`
	WordCount   int    `json:"wordCount"`
	PageCount   int    `json:"pageCount"`
	Chapters    int    `json:"chapters"`
	HasCover    bool   `json:"hasCover"`

	Cover     []byte `json:"-"`
	CoverMime string `json:"-"`
}

type container struct {
	Rootfiles []struct {
		FullPath  string `xml:"full-path,attr"`
		MediaType string `xml:"media-type,attr"`
	} `xml:"rootfiles>rootfile"`
}

type opf struct {
	Metadata struct {
		Titles       []string `xml:"title"`
		Creators     []string `xml:"creator"`
		Languages    []string `xml:"language"`
		Descriptions []string `xml:"description"`
		Metas        []struct {
			Name    string `xml:"name,attr"`
			Content string `xml:"content,attr"`
		} `xml:"meta"`
	} `xml:"metadata"`
	Manifest []struct {
		ID         string `xml:"id,attr"`
		Href       string `xml:"href,attr"`
		MediaType  string `xml:"media-type,attr"`
		Properties string `xml:"properties,attr"`
	} `xml:"manifest>item"`
	Spine []struct {
		IDRef string `xml:"idref,attr"`
	} `xml:"spine>itemref"`
}

// Parse validates data as an EPUB and returns its metadata.
func Parse(data []byte) (*Metadata, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("%w: not a zip archive", ErrInvalid)
	}
	files := make(map[string]*zip.File, len(zr.File))
	for _, f := range zr.File {
		files[f.Name] = f
	}

	mt, err := readEntry(files, "mimetype")
	if err != nil || strings.TrimSpace(string(mt)) != "application/epub+zip" {
		return nil, fmt.Errorf("%w: missing or wrong mimetype entry", ErrInvalid)
	}

	cBytes, err := readEntry(files, "META-INF/container.xml")
	if err != nil {
		return nil, fmt.Errorf("%w: missing META-INF/container.xml", ErrInvalid)
	}
	var c container
	if err := xml.Unmarshal(cBytes, &c); err != nil || len(c.Rootfiles) == 0 {
		return nil, fmt.Errorf("%w: unreadable container.xml", ErrInvalid)
	}
	opfPath := c.Rootfiles[0].FullPath
	for _, rf := range c.Rootfiles {
		if rf.MediaType == "application/oebps-package+xml" {
			opfPath = rf.FullPath
			break
		}
	}

	opfBytes, err := readEntry(files, opfPath)
	if err != nil {
		return nil, fmt.Errorf("%w: package document %q not found", ErrInvalid, opfPath)
	}
	var pkg opf
	if err := xml.Unmarshal(opfBytes, &pkg); err != nil {
		return nil, fmt.Errorf("%w: unreadable package document", ErrInvalid)
	}
	if len(pkg.Spine) == 0 {
		return nil, fmt.Errorf("%w: package has no reading order (spine)", ErrInvalid)
	}

	base := path.Dir(opfPath)
	resolve := func(href string) string {
		if u, err := url.PathUnescape(href); err == nil {
			href = u
		}
		if i := strings.IndexByte(href, '#'); i >= 0 {
			href = href[:i]
		}
		if base == "." {
			return path.Clean(href)
		}
		return path.Join(base, href)
	}

	md := &Metadata{
		Title:       clean(first(pkg.Metadata.Titles)),
		Author:      clean(strings.Join(nonEmpty(pkg.Metadata.Creators), ", ")),
		Language:    clean(first(pkg.Metadata.Languages)),
		Description: stripTags(first(pkg.Metadata.Descriptions)),
	}

	manifest := make(map[string]int, len(pkg.Manifest))
	for i, it := range pkg.Manifest {
		manifest[it.ID] = i
	}

	// Word count across the reading order.
	for _, ref := range pkg.Spine {
		i, ok := manifest[ref.IDRef]
		if !ok {
			continue
		}
		it := pkg.Manifest[i]
		if it.MediaType != "application/xhtml+xml" && it.MediaType != "text/html" {
			continue
		}
		doc, err := readEntry(files, resolve(it.Href))
		if err != nil {
			continue
		}
		md.Chapters++
		md.WordCount += countWords(doc)
	}
	if md.WordCount > 0 {
		md.PageCount = (md.WordCount + WordsPerPage - 1) / WordsPerPage
	}

	// Cover: EPUB3 "cover-image" property, then EPUB2 <meta name="cover">,
	// then any image whose id or href mentions "cover".
	coverIdx := -1
	for i, it := range pkg.Manifest {
		if hasToken(it.Properties, "cover-image") {
			coverIdx = i
			break
		}
	}
	if coverIdx < 0 {
		for _, m := range pkg.Metadata.Metas {
			if m.Name == "cover" {
				if i, ok := manifest[m.Content]; ok && strings.HasPrefix(pkg.Manifest[i].MediaType, "image/") {
					coverIdx = i
				}
			}
		}
	}
	if coverIdx < 0 {
		for i, it := range pkg.Manifest {
			if strings.HasPrefix(it.MediaType, "image/") &&
				(strings.Contains(strings.ToLower(it.ID), "cover") || strings.Contains(strings.ToLower(it.Href), "cover")) {
				coverIdx = i
				break
			}
		}
	}
	if coverIdx >= 0 {
		it := pkg.Manifest[coverIdx]
		if img, err := readEntry(files, resolve(it.Href)); err == nil && len(img) > 0 {
			md.Cover, md.CoverMime, md.HasCover = img, it.MediaType, true
		}
	}

	return md, nil
}

func readEntry(files map[string]*zip.File, name string) ([]byte, error) {
	f, ok := files[name]
	if !ok {
		return nil, fmt.Errorf("entry %q not found", name)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, maxEntryBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxEntryBytes {
		return nil, fmt.Errorf("entry %q too large", name)
	}
	return b, nil
}

var skipElements = map[string]bool{"script": true, "style": true, "head": true}

// countWords counts words in the text content of an (X)HTML document.
func countWords(doc []byte) int {
	dec := xml.NewDecoder(bytes.NewReader(doc))
	dec.Strict = false
	dec.AutoClose = xml.HTMLAutoClose
	dec.Entity = xml.HTMLEntity
	words, skip := 0, 0
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if skipElements[strings.ToLower(t.Name.Local)] {
				skip++
			}
		case xml.EndElement:
			if skipElements[strings.ToLower(t.Name.Local)] && skip > 0 {
				skip--
			}
		case xml.CharData:
			if skip == 0 {
				words += len(strings.FieldsFunc(string(t), func(r rune) bool {
					return unicode.IsSpace(r)
				}))
			}
		}
	}
	return words
}

var tagRE = regexp.MustCompile(`<[^>]*>`)

func stripTags(s string) string {
	s = tagRE.ReplaceAllString(s, " ")
	r := strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'", "&nbsp;", " ")
	return clean(r.Replace(s))
}

func clean(s string) string { return strings.Join(strings.Fields(s), " ") }

func first(ss []string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

func nonEmpty(ss []string) []string {
	var out []string
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}

func hasToken(list, tok string) bool {
	for _, t := range strings.Fields(list) {
		if t == tok {
			return true
		}
	}
	return false
}
