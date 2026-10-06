package epub

import (
	"archive/zip"
	"bytes"
	"errors"
	"testing"
)

// Build creates a minimal EPUB in memory. Exported for use by other packages' tests
// through the epubtest helper below.
func build(t *testing.T, files map[string]string, mimetype string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.CreateHeader(&zip.FileHeader{Name: "mimetype", Method: zip.Store})
	_, _ = w.Write([]byte(mimetype))
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestParse(t *testing.T) {
	data := build(t, SampleFiles("The Glass House", "Elena Vance"), "application/epub+zip")
	md, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if md.Title != "The Glass House" || md.Author != "Elena Vance" || md.Language != "en" {
		t.Errorf("unexpected metadata: %+v", md)
	}
	if md.Description != "A house of secrets & glass." {
		t.Errorf("description = %q", md.Description)
	}
	if md.Chapters != 2 {
		t.Errorf("chapters = %d, want 2", md.Chapters)
	}
	// 10 words in chapter 1 (heading + paragraph) + 4 in chapter 2; head and script are ignored.
	if md.WordCount != 14 {
		t.Errorf("wordCount = %d, want 14", md.WordCount)
	}
	if md.PageCount != 1 {
		t.Errorf("pageCount = %d, want 1", md.PageCount)
	}
	if !md.HasCover || md.CoverMime != "image/png" || len(md.Cover) == 0 {
		t.Errorf("cover not extracted: has=%v mime=%q len=%d", md.HasCover, md.CoverMime, len(md.Cover))
	}
}

func TestParseRejectsInvalid(t *testing.T) {
	cases := map[string][]byte{
		"not zip":      []byte("hello"),
		"wrong mime":   build(t, SampleFiles("x", "y"), "application/zip"),
		"no container": build(t, map[string]string{"a.txt": "x"}, "application/epub+zip"),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(data); !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
		})
	}
}
