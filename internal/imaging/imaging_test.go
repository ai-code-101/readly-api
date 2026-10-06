package imaging

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

func TestThumbnail(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 800, 1200))
	for x := 0; x < 800; x++ {
		src.Set(x, 10, color.RGBA{200, 0, 0, 255})
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		t.Fatal(err)
	}
	if mt, err := Sniff(buf.Bytes()); err != nil || mt != "image/png" {
		t.Fatalf("Sniff = %q, %v", mt, err)
	}
	out, err := Thumbnail(buf.Bytes(), 400)
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if got := img.Bounds().Size(); got.X != 400 || got.Y != 600 {
		t.Fatalf("thumbnail size = %v, want 400x600", got)
	}
}

func TestSniffRejectsNonImages(t *testing.T) {
	if _, err := Sniff([]byte("<html></html>")); err != ErrUnsupported {
		t.Fatalf("err = %v", err)
	}
}
