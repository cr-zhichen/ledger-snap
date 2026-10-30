// Package imgutil resizes screenshots for model input.
package imgutil

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png"

	"golang.org/x/image/draw"
)

// Fit decodes a JPEG or PNG and re-encodes it as JPEG so that the longer side
// is at most maxSide and the shorter side at least minSide.
func Fit(data []byte, maxSide, minSide, quality int) ([]byte, error) {
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("无法解码图片: %w", err)
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	scale := 1.0
	if long := max(w, h); maxSide > 0 && long > maxSide {
		scale = float64(maxSide) / float64(long)
	}
	if short := float64(min(w, h)) * scale; minSide > 0 && short < float64(minSide) {
		scale = float64(minSide) / float64(min(w, h))
	}
	dst := src
	if scale != 1 {
		nw, nh := max(1, int(float64(w)*scale+0.5)), max(1, int(float64(h)*scale+0.5))
		rgba := image.NewRGBA(image.Rect(0, 0, nw, nh))
		draw.CatmullRom.Scale(rgba, rgba.Bounds(), src, b, draw.Src, nil)
		dst = rgba
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
