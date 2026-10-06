// Package imaging validates uploaded images and produces JPEG thumbnails.
package imaging

import (
	"bytes"
	"errors"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"net/http"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

var ErrUnsupported = errors.New("unsupported image: use JPEG, PNG, WebP or GIF")

var allowed = map[string]bool{
	"image/jpeg": true, "image/png": true, "image/webp": true, "image/gif": true,
}

// Sniff returns the detected MIME type of data, or ErrUnsupported if it is not
// one of the accepted image formats.
func Sniff(data []byte) (string, error) {
	mt := http.DetectContentType(data)
	if !allowed[mt] {
		return "", ErrUnsupported
	}
	return mt, nil
}

// Thumbnail decodes data and returns a JPEG no wider than maxWidth, preserving
// the aspect ratio. Images already narrower than maxWidth are re-encoded as-is.
func Thumbnail(data []byte, maxWidth int) ([]byte, error) {
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, ErrUnsupported
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w > maxWidth {
		h = h * maxWidth / w
		w = maxWidth
	}
	if h < 1 {
		h = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	// White background so transparent PNGs don't turn black as JPEG.
	draw.Draw(dst, dst.Bounds(), image.White, image.Point{}, draw.Src)
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Over, nil)
	var out bytes.Buffer
	if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: 85}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
