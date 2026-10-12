package qrscan

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"testing"

	"rsc.io/qr"
)

const uri = "otpauth://totp/LinOTP:bd346?secret=JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP&issuer=LinOTP"

// screenshot puts a QR code of text, scale pixels per module, at (x, y) on a page-like picture.
func screenshot(t *testing.T, text string, scale, x, y int) *image.RGBA {
	t.Helper()
	code, err := qr.Encode(text, qr.M)
	if err != nil {
		t.Fatal(err)
	}
	page := image.NewRGBA(image.Rect(0, 0, 1280, 800))
	draw.Draw(page, page.Bounds(), &image.Uniform{color.RGBA{0xf4, 0xf4, 0xf4, 0xff}}, image.Point{}, draw.Src)
	// a dialog, a red button and some "text" around the code, like the portal
	draw.Draw(page, image.Rect(100, 80, 1180, 720), image.White, image.Point{}, draw.Src)
	draw.Draw(page, image.Rect(1000, 640, 1150, 690), &image.Uniform{color.RGBA{0xc8, 0x10, 0x2e, 0xff}}, image.Point{}, draw.Src)
	for row := 0; row < 8; row++ {
		draw.Draw(page, image.Rect(600, 200+row*28, 1100, 212+row*28), &image.Uniform{color.RGBA{0xd0, 0x20, 0x20, 0xff}}, image.Point{}, draw.Src)
	}
	quiet := 4 * scale
	for i := 0; i < code.Size; i++ {
		for j := 0; j < code.Size; j++ {
			if code.Black(i, j) {
				r := image.Rect(x+quiet+i*scale, y+quiet+j*scale, x+quiet+(i+1)*scale, y+quiet+(j+1)*scale)
				draw.Draw(page, r, image.Black, image.Point{}, draw.Src)
			}
		}
	}
	return page
}

func encodePNG(t *testing.T, img image.Image) []byte {
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestTextsScreenshotPNG(t *testing.T) {
	texts, err := Texts(encodePNG(t, screenshot(t, uri, 4, 180, 220)))
	if err != nil {
		t.Fatal(err)
	}
	if len(texts) != 1 || texts[0] != uri {
		t.Fatalf("texts = %q", texts)
	}
}

func TestTextsSmallCodeJPEG(t *testing.T) {
	var b bytes.Buffer
	if err := jpeg.Encode(&b, screenshot(t, uri, 3, 180, 220), &jpeg.Options{Quality: 80}); err != nil {
		t.Fatal(err)
	}
	texts, err := Texts(b.Bytes())
	if err != nil || len(texts) == 0 || texts[0] != uri {
		t.Fatalf("texts = %q, %v", texts, err)
	}
}

func TestTextsNoCode(t *testing.T) {
	page := image.NewRGBA(image.Rect(0, 0, 300, 200))
	draw.Draw(page, page.Bounds(), image.White, image.Point{}, draw.Src)
	if _, err := Texts(encodePNG(t, page)); !errors.Is(err, ErrNoCode) {
		t.Fatalf("err = %v", err)
	}
}

func TestTextsNotPicture(t *testing.T) {
	for _, data := range [][]byte{nil, []byte("otpauth://totp/x?secret=ABC"), []byte("\x89PNG\r\n\x1a\nbroken")} {
		if _, err := Texts(data); !errors.Is(err, ErrNotPicture) {
			t.Fatalf("%q: err = %v", data, err)
		}
	}
}
