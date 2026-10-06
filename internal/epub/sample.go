package epub

import (
	"archive/zip"
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
)

// sampleCover is a plain Readly-green 60x90 PNG used as the sample book cover.
var sampleCover = func() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 60, 90))
	for y := 0; y < 90; y++ {
		for x := 0; x < 60; x++ {
			img.Set(x, y, color.RGBA{0x0b, 0x4a, 0x3b, 0xff})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}()

// SampleFiles returns the entries (excluding "mimetype") of a small two-chapter
// EPUB 3 book. It is used by tests and by the seed command.
func SampleFiles(title, author string) map[string]string {
	return map[string]string{
		"META-INF/container.xml": `<?xml version="1.0"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles>
</container>`,
		"OEBPS/content.opf": fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="uid">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:identifier id="uid">urn:uuid:readly-sample</dc:identifier>
    <dc:title>%s</dc:title>
    <dc:creator>%s</dc:creator>
    <dc:language>en</dc:language>
    <dc:description>&lt;p&gt;A house of secrets &amp;amp; glass.&lt;/p&gt;</dc:description>
  </metadata>
  <manifest>
    <item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>
    <item id="c1" href="text/chapter%%201.xhtml" media-type="application/xhtml+xml"/>
    <item id="c2" href="text/chapter2.xhtml" media-type="application/xhtml+xml"/>
    <item id="img" href="images/cover.png" media-type="image/png" properties="cover-image"/>
  </manifest>
  <spine><itemref idref="c1"/><itemref idref="c2"/></spine>
</package>`, title, author),
		"OEBPS/nav.xhtml": `<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops"><head><title>Contents</title></head>
<body><nav epub:type="toc"><ol>
<li><a href="text/chapter%201.xhtml">Chapter 1: The Hidden Room</a></li>
<li><a href="text/chapter2.xhtml">Chapter 2: Moonlight</a></li>
</ol></nav></body></html>`,
		"OEBPS/text/chapter 1.xhtml": `<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml"><head><title>Chapter 1</title><style>p{}</style></head>
<body><h1>The Hidden Room</h1><p>The lantern light flickered against the glass.</p>
<script>var ignored = "these words do not count";</script></body></html>`,
		"OEBPS/text/chapter2.xhtml": `<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml"><head><title>Chapter 2</title></head>
<body><p>Elena&nbsp;held her breath.</p></body></html>`,
		"OEBPS/images/cover.png": string(sampleCover),
	}
}

// BuildSample returns a complete, valid sample EPUB.
func BuildSample(title, author string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.CreateHeader(&zip.FileHeader{Name: "mimetype", Method: zip.Store})
	_, _ = w.Write([]byte("application/epub+zip"))
	for name, body := range SampleFiles(title, author) {
		w, _ := zw.Create(name)
		_, _ = w.Write([]byte(body))
	}
	_ = zw.Close()
	return buf.Bytes()
}
