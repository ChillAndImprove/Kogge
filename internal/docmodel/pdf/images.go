// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

package pdf

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	stddraw "image/draw"
	"image/gif"
	"image/jpeg"
	"image/png"
	"strings"

	"codeberg.org/go-pdf/fpdf"
	"golang.org/x/image/draw"
	"golang.org/x/image/webp"
)

// Images: PNG, JPEG, GIF (the first frame) and WebP are embedded; images whose PDF form fpdf does not read
// (interlaced or 16-bit PNG, CMYK JPEG, palettes) are converted, and images with more pixels than print needs
// are scaled down. SVG cannot be drawn without a renderer for it: the PDF names the image and links to it.

const (
	maxImageBytes = 25 << 20  // larger files are not embedded
	maxPixels     = 2400      // the longer side of an embedded image (300 dpi across a page)
	pxMM          = 25.4 / 96 // one CSS pixel, as the editor sizes images
	// maxDecodePixels bounds the images that are decoded to be scaled down: decoding takes memory for every pixel,
	// and a small file can claim billions of them (a PNG of zeros compresses about a thousand times). Photographs
	// of today's cameras stay below it.
	maxDecodePixels = 50_000_000
)

// decodable checks, before an image is decoded, that its pixels are within the bound.
func decodable(cfg image.Config) error {
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > maxDecodePixels {
		return fmt.Errorf("pdf: the image has %d × %d pixels, more than are decoded", cfg.Width, cfg.Height)
	}
	return nil
}

// placedImage is an image registered in the PDF with its size on the page (millimetres).
type placedImage struct {
	name    string
	pxW     int
	pxH     int
	w, h    float64
	svgName string // an SVG image: shown as its name
	url     string // where an image that is not shown can be opened
}

// image registers the image of an attachment or source once, or returns nil when it cannot be shown.
func (r *renderer) image(attachment int64, src string) *placedImage {
	key := fmt.Sprintf("%d|%s", attachment, src)
	if img, ok := r.images[key]; ok {
		return img
	}
	r.images[key] = nil
	res := r.res.Image(attachment, src)
	if res == nil || len(res.Data) == 0 {
		return nil
	}
	if strings.Contains(res.Type, "svg") {
		img := &placedImage{svgName: res.Name, url: res.URL}
		r.images[key] = img
		return img
	}
	if len(res.Data) > maxImageBytes || r.imageBytes+len(res.Data) > r.maxImageTotal {
		return nil
	}
	data, typ, w, h, err := prepareImage(res.Data)
	if err != nil {
		return nil
	}
	name := fmt.Sprintf("img%d", len(r.images))
	r.f.RegisterImageOptionsReader(name, fpdf.ImageOptions{ImageType: typ}, bytes.NewReader(data))
	if r.f.Err() {
		// fpdf refused the image after all; the document goes on without it.
		r.f.ClearError()
		return nil
	}
	r.imageBytes += len(data)
	img := &placedImage{name: name, pxW: w, pxH: h, w: float64(w) * pxMM, h: float64(h) * pxMM}
	r.images[key] = img
	return img
}

func (r *renderer) drawImage(img *placedImage, x, y float64) {
	r.f.ImageOptions(img.name, x, y, img.w, img.h, false, fpdf.ImageOptions{}, 0, "")
}

// sized returns the image at a width (0: its own), within a box of maxW by maxH, keeping its proportions.
func (img *placedImage) sized(width, maxW, maxH float64) *placedImage {
	out := *img
	if width > 0 {
		out.w = width
		out.h = img.h * width / img.w
	}
	if out.w > maxW {
		out.h *= maxW / out.w
		out.w = maxW
	}
	if out.h > maxH {
		out.w *= maxH / out.h
		out.h = maxH
	}
	return &out
}

// prepareImage returns image data in a form fpdf embeds (its type for fpdf), with its size in pixels.
func prepareImage(data []byte) ([]byte, string, int, int, error) {
	switch sniff(data) {
	case "jpg":
		cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return nil, "", 0, 0, err
		}
		// fpdf embeds JPEG as it is, but not CMYK as Adobe's programs write it (inverted).
		if cfg.Width <= maxPixels && cfg.Height <= maxPixels && (cfg.ColorModel == color.YCbCrModel || cfg.ColorModel == color.GrayModel) {
			return data, "jpg", cfg.Width, cfg.Height, nil
		}
		if err := decodable(cfg); err != nil {
			return nil, "", 0, 0, err
		}
		img, err := jpeg.Decode(bytes.NewReader(data))
		if err != nil {
			return nil, "", 0, 0, err
		}
		return encode(scaled(img), true)
	case "png":
		cfg, err := png.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return nil, "", 0, 0, err
		}
		if simplePNG(data) && cfg.Width <= maxPixels && cfg.Height <= maxPixels {
			return data, "png", cfg.Width, cfg.Height, nil
		}
		if err := decodable(cfg); err != nil {
			return nil, "", 0, 0, err
		}
		img, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			return nil, "", 0, 0, err
		}
		return encode(scaled(img), false)
	case "gif":
		// The first frame lies within the image's bounds, which the configuration has.
		cfg, err := gif.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return nil, "", 0, 0, err
		}
		if err := decodable(cfg); err != nil {
			return nil, "", 0, 0, err
		}
		img, err := gif.Decode(bytes.NewReader(data))
		if err != nil {
			return nil, "", 0, 0, err
		}
		return encode(scaled(img), false)
	case "webp":
		cfg, err := webp.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return nil, "", 0, 0, err
		}
		if err := decodable(cfg); err != nil {
			return nil, "", 0, 0, err
		}
		img, err := webp.Decode(bytes.NewReader(data))
		if err != nil {
			return nil, "", 0, 0, err
		}
		return encode(scaled(img), false)
	}
	return nil, "", 0, 0, fmt.Errorf("pdf: the image is not PNG, JPEG, GIF or WebP")
}

// sniff tells the format of image data by its first bytes (the type an uploader gave may be wrong).
func sniff(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return "png"
	case bytes.HasPrefix(data, []byte("\xff\xd8\xff")):
		return "jpg"
	case bytes.HasPrefix(data, []byte("GIF87a")), bytes.HasPrefix(data, []byte("GIF89a")):
		return "gif"
	case len(data) > 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return "webp"
	}
	return ""
}

// simplePNG reports whether fpdf reads a PNG as it is: 8 bits per channel, grey or RGB with or without alpha,
// not interlaced.
func simplePNG(data []byte) bool {
	if len(data) < 33 || string(data[12:16]) != "IHDR" {
		return false
	}
	depth, colorType, interlace := data[24], data[25], data[28]
	return depth == 8 && interlace == 0 && (colorType == 0 || colorType == 2 || colorType == 4 || colorType == 6)
}

// scaled returns an image with at most maxPixels on its longer side.
func scaled(img image.Image) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= maxPixels && h <= maxPixels {
		return img
	}
	if w >= h {
		h, w = max(1, h*maxPixels/w), maxPixels
	} else {
		w, h = max(1, w*maxPixels/h), maxPixels
	}
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
	return dst
}

// encode writes an image as JPEG (photographs) or as an 8-bit PNG.
func encode(img image.Image, photo bool) ([]byte, string, int, int, error) {
	b := img.Bounds()
	var buf bytes.Buffer
	if photo {
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 88}); err != nil {
			return nil, "", 0, 0, err
		}
		return buf.Bytes(), "jpg", b.Dx(), b.Dy(), nil
	}
	nrgba, ok := img.(*image.NRGBA)
	if !ok {
		nrgba = image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
		stddraw.Draw(nrgba, nrgba.Bounds(), img, b.Min, stddraw.Src)
	}
	if err := png.Encode(&buf, nrgba); err != nil {
		return nil, "", 0, 0, err
	}
	return buf.Bytes(), "png", b.Dx(), b.Dy(), nil
}
