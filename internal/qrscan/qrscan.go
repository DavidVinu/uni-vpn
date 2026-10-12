// Package qrscan reads QR codes from a picture, for example a screenshot of the MFA portal's
// setup page. Role model: 1Password and Bitwarden, which add a one-time code from a QR code on
// the screen instead of making the user copy its text.
package qrscan

import (
	"bytes"
	"errors"
	"image"
	_ "image/gif"  // decoders for image.Decode
	_ "image/jpeg" //
	_ "image/png"  //

	"github.com/makiuchi-d/gozxing"
	multiqr "github.com/makiuchi-d/gozxing/multi/qrcode"
)

// MaxPixels bounds the decoded picture: a 6K screen is about 20 million pixels.
const MaxPixels = 40_000_000

var (
	// ErrNotPicture: the data is no PNG, JPEG or GIF picture.
	ErrNotPicture = errors.New("not a picture")
	// ErrNoCode: the picture has no readable QR code.
	ErrNoCode = errors.New("no QR code found")
)

// Texts returns the text of every QR code in the picture, in the order they were found.
func Texts(data []byte) ([]string, error) {
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width <= 0 || config.Height <= 0 {
		return nil, ErrNotPicture
	}
	if config.Width*config.Height > MaxPixels {
		return nil, ErrNotPicture
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, ErrNotPicture
	}
	bitmap, err := gozxing.NewBinaryBitmapFromImage(img)
	if err != nil {
		return nil, ErrNotPicture
	}
	hints := map[gozxing.DecodeHintType]any{gozxing.DecodeHintType_TRY_HARDER: true}
	results, err := multiqr.NewQRCodeMultiReader().DecodeMultiple(bitmap, hints)
	if err != nil || len(results) == 0 {
		return nil, ErrNoCode
	}
	texts := make([]string, 0, len(results))
	for _, r := range results {
		texts = append(texts, r.GetText())
	}
	return texts, nil
}
