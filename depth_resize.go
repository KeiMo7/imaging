package imaging

import (
	"image"
	"math"
)

// ResizeWithDepth resizes img without passing through an 8-bit intermediate
// for 10-bit and 16-bit output. If WithBitDepth is omitted, the input depth is
// preserved.
func ResizeWithDepth(img image.Image, width, height int, filter ResampleFilter, opts ...DepthOption) (image.Image, error) {
	cfg, err := applyDepthOptions(opts...)
	if err != nil {
		return nil, err
	}
	if err := cfg.begin("resize", img); err != nil {
		cfg.logFail("resize", err)
		return nil, err
	}
	dst, err := resizeAtDepth(img, width, height, filter, cfg)
	if err != nil {
		cfg.logFail("resize", err)
		return nil, err
	}
	cfg.logExit("resize")
	return dst, nil
}

// ResizePreserveDepth resizes img and preserves its detected input depth.
func ResizePreserveDepth(img image.Image, width, height int, filter ResampleFilter, opts ...DepthOption) (image.Image, error) {
	return ResizeWithDepth(img, width, height, filter, opts...)
}

// Resize10 resizes img to a 10-bit NRGBA64 image.
func Resize10(img image.Image, width, height int, filter ResampleFilter, opts ...DepthOption) (*image.NRGBA64, error) {
	opts = append([]DepthOption{WithBitDepth(BitDepth10)}, opts...)
	dst, err := ResizeWithDepth(img, width, height, filter, opts...)
	if err != nil {
		return nil, err
	}
	return dst.(*image.NRGBA64), nil
}

// ResizeBatchItem describes one item in a mixed-depth resize batch.
type ResizeBatchItem struct {
	Image   image.Image
	Width   int
	Height  int
	Filter  ResampleFilter
	Options []DepthOption
}

// ResizeBatchResult is the result for one batch item.
type ResizeBatchResult struct {
	Image    image.Image
	BitDepth BitDepth
	Err      error
}

// ResizeBatchWithDepth resolves the bit depth independently for every item.
// A failure in one item does not force the rest of the batch to a lower depth.
func ResizeBatchWithDepth(items []ResizeBatchItem, opts ...DepthOption) []ResizeBatchResult {
	results := make([]ResizeBatchResult, len(items))
	for i, item := range items {
		itemOpts := make([]DepthOption, 0, len(opts)+len(item.Options))
		itemOpts = append(itemOpts, opts...)
		itemOpts = append(itemOpts, item.Options...)
		dst, err := ResizeWithDepth(item.Image, item.Width, item.Height, item.Filter, itemOpts...)
		results[i].Image = dst
		results[i].Err = err
		if err == nil && dst != nil {
			results[i].BitDepth = DetectBitDepth(dst)
		}
	}
	return results
}

func resizeAtDepth(img image.Image, width, height int, filter ResampleFilter, cfg depthConfig) (image.Image, error) {
	dstW, dstH := width, height
	if dstW < 0 || dstH < 0 || (dstW == 0 && dstH == 0) {
		return newDepthImage(0, 0, cfg.targetResolved), nil
	}

	srcW := img.Bounds().Dx()
	srcH := img.Bounds().Dy()
	if srcW <= 0 || srcH <= 0 {
		return newDepthImage(0, 0, cfg.targetResolved), nil
	}

	if dstW == 0 {
		tmpW := float64(dstH) * float64(srcW) / float64(srcH)
		dstW = int(math.Max(1.0, math.Floor(tmpW+0.5)))
	}
	if dstH == 0 {
		tmpH := float64(dstW) * float64(srcH) / float64(srcW)
		dstH = int(math.Max(1.0, math.Floor(tmpH+0.5)))
	}

	if srcW == dstW && srcH == dstH {
		if cfg.targetResolved == BitDepth8 && cfg.dither == DitherNone && cfg.input == BitDepth8 {
			return Clone(img), nil
		}
		return cloneAtDepth(img, cfg.targetResolved, cfg.dither)
	}

	if cfg.targetResolved == BitDepth8 && cfg.dither == DitherNone && cfg.input == BitDepth8 {
		return Resize(img, dstW, dstH, filter), nil
	}

	if filter.Support <= 0 {
		return resizeNearestAtDepth(img, dstW, dstH, cfg), nil
	}
	if srcW != dstW && srcH != dstH {
		tmp := resizeHorizontalAtDepth(img, dstW, filter, cfg)
		return resizeVerticalAtDepth(tmp, dstH, filter, cfg), nil
	}
	if srcW != dstW {
		return resizeHorizontalAtDepth(img, dstW, filter, cfg), nil
	}
	return resizeVerticalAtDepth(img, dstH, filter, cfg), nil
}

func resizeHorizontalAtDepth(img image.Image, width int, filter ResampleFilter, cfg depthConfig) image.Image {
	src := newScanner64(img)
	dst := newDepthImage(width, src.h, cfg.targetResolved)
	weights := precomputeWeights(width, src.w, filter)
	parallel(0, src.h, func(ys <-chan int) {
		scanLine := make([]uint16, src.w*4)
		for y := range ys {
			src.scan(0, y, src.w, y+1, scanLine)
			for x := range weights {
				var r, g, b, a float64
				for _, w := range weights[x] {
					i := w.index * 4
					s := scanLine[i : i+4 : i+4]
					aw := float64(s[3]) * w.weight
					r += float64(s[0]) * aw
					g += float64(s[1]) * aw
					b += float64(s[2]) * aw
					a += aw
				}
				if a != 0 {
					aInv := 1 / a
					writeDepthPixel(dst, x, y,
						clamp16(r*aInv), clamp16(g*aInv), clamp16(b*aInv), clamp16(a),
						cfg.targetResolved, cfg.dither)
				}
			}
		}
	})
	return dst
}

func resizeVerticalAtDepth(img image.Image, height int, filter ResampleFilter, cfg depthConfig) image.Image {
	src := newScanner64(img)
	dst := newDepthImage(src.w, height, cfg.targetResolved)
	weights := precomputeWeights(height, src.h, filter)
	parallel(0, src.w, func(xs <-chan int) {
		scanLine := make([]uint16, src.h*4)
		for x := range xs {
			src.scan(x, 0, x+1, src.h, scanLine)
			for y := range weights {
				var r, g, b, a float64
				for _, w := range weights[y] {
					i := w.index * 4
					s := scanLine[i : i+4 : i+4]
					aw := float64(s[3]) * w.weight
					r += float64(s[0]) * aw
					g += float64(s[1]) * aw
					b += float64(s[2]) * aw
					a += aw
				}
				if a != 0 {
					aInv := 1 / a
					writeDepthPixel(dst, x, y,
						clamp16(r*aInv), clamp16(g*aInv), clamp16(b*aInv), clamp16(a),
						cfg.targetResolved, cfg.dither)
				}
			}
		}
	})
	return dst
}

func resizeNearestAtDepth(img image.Image, width, height int, cfg depthConfig) image.Image {
	src := newScanner64(img)
	dst := newDepthImage(width, height, cfg.targetResolved)
	dx := float64(img.Bounds().Dx()) / float64(width)
	dy := float64(img.Bounds().Dy()) / float64(height)

	parallel(0, height, func(ys <-chan int) {
		var pixel [4]uint16
		for y := range ys {
			srcY := int((float64(y) + 0.5) * dy)
			for x := 0; x < width; x++ {
				srcX := int((float64(x) + 0.5) * dx)
				src.scan(srcX, srcY, srcX+1, srcY+1, pixel[:])
				writeDepthPixel(dst, x, y, pixel[0], pixel[1], pixel[2], pixel[3],
					cfg.targetResolved, cfg.dither)
			}
		}
	})
	return dst
}

// FitWithDepth scales down img to fit the requested bounds while preserving
// its input depth unless an explicit target is supplied.
func FitWithDepth(img image.Image, width, height int, filter ResampleFilter, opts ...DepthOption) (image.Image, error) {
	cfg, err := applyDepthOptions(opts...)
	if err != nil {
		return nil, err
	}
	if err := cfg.begin("fit", img); err != nil {
		cfg.logFail("fit", err)
		return nil, err
	}
	dst, err := fitAtDepth(img, width, height, filter, cfg)
	if err != nil {
		cfg.logFail("fit", err)
		return nil, err
	}
	cfg.logExit("fit")
	return dst, nil
}

// Fit10 scales down img to a 10-bit image that fits the requested bounds.
func Fit10(img image.Image, width, height int, filter ResampleFilter, opts ...DepthOption) (*image.NRGBA64, error) {
	opts = append([]DepthOption{WithBitDepth(BitDepth10)}, opts...)
	dst, err := FitWithDepth(img, width, height, filter, opts...)
	if err != nil {
		return nil, err
	}
	return dst.(*image.NRGBA64), nil
}

func fitAtDepth(img image.Image, width, height int, filter ResampleFilter, cfg depthConfig) (image.Image, error) {
	maxW, maxH := width, height
	if maxW <= 0 || maxH <= 0 {
		return newDepthImage(0, 0, cfg.targetResolved), nil
	}

	srcW := img.Bounds().Dx()
	srcH := img.Bounds().Dy()
	if srcW <= 0 || srcH <= 0 {
		return newDepthImage(0, 0, cfg.targetResolved), nil
	}
	if srcW <= maxW && srcH <= maxH {
		return cloneAtDepth(img, cfg.targetResolved, cfg.dither)
	}

	srcAspectRatio := float64(srcW) / float64(srcH)
	maxAspectRatio := float64(maxW) / float64(maxH)
	var newW, newH int
	if srcAspectRatio > maxAspectRatio {
		newW = maxW
		newH = int(float64(newW) / srcAspectRatio)
	} else {
		newH = maxH
		newW = int(float64(newH) * srcAspectRatio)
	}
	return resizeAtDepth(img, newW, newH, filter, cfg)
}

// FillWithDepth scales and crops img to the requested dimensions while
// preserving its input depth unless an explicit target is supplied.
func FillWithDepth(img image.Image, width, height int, anchor Anchor, filter ResampleFilter, opts ...DepthOption) (image.Image, error) {
	cfg, err := applyDepthOptions(opts...)
	if err != nil {
		return nil, err
	}
	if err := cfg.begin("fill", img); err != nil {
		cfg.logFail("fill", err)
		return nil, err
	}
	dst, err := fillAtDepth(img, width, height, anchor, filter, cfg)
	if err != nil {
		cfg.logFail("fill", err)
		return nil, err
	}
	cfg.logExit("fill")
	return dst, nil
}

// Fill10 scales and crops img to a 10-bit image with the requested dimensions.
func Fill10(img image.Image, width, height int, anchor Anchor, filter ResampleFilter, opts ...DepthOption) (*image.NRGBA64, error) {
	opts = append([]DepthOption{WithBitDepth(BitDepth10)}, opts...)
	dst, err := FillWithDepth(img, width, height, anchor, filter, opts...)
	if err != nil {
		return nil, err
	}
	return dst.(*image.NRGBA64), nil
}

func fillAtDepth(img image.Image, width, height int, anchor Anchor, filter ResampleFilter, cfg depthConfig) (image.Image, error) {
	if width <= 0 || height <= 0 {
		return newDepthImage(0, 0, cfg.targetResolved), nil
	}
	srcW := img.Bounds().Dx()
	srcH := img.Bounds().Dy()
	if srcW <= 0 || srcH <= 0 {
		return newDepthImage(0, 0, cfg.targetResolved), nil
	}
	if srcW == width && srcH == height {
		return cloneAtDepth(img, cfg.targetResolved, cfg.dither)
	}
	if srcW >= 100 && srcH >= 100 {
		return cropAndResizeAtDepth(img, width, height, anchor, filter, cfg)
	}
	return resizeAndCropAtDepth(img, width, height, anchor, filter, cfg)
}

func cropAndResizeAtDepth(img image.Image, width, height int, anchor Anchor, filter ResampleFilter, cfg depthConfig) (image.Image, error) {
	srcW := img.Bounds().Dx()
	srcH := img.Bounds().Dy()
	srcAspectRatio := float64(srcW) / float64(srcH)
	dstAspectRatio := float64(width) / float64(height)

	var tmp image.Image
	if srcAspectRatio < dstAspectRatio {
		cropH := float64(srcW) * float64(height) / float64(width)
		tmp = cropAtDepth(img, CropAnchorRect(img, srcW, int(math.Max(1, cropH)+0.5), anchor), cfg)
	} else {
		cropW := float64(srcH) * float64(width) / float64(height)
		tmp = cropAtDepth(img, CropAnchorRect(img, int(math.Max(1, cropW)+0.5), srcH, anchor), cfg)
	}
	return resizeAtDepth(tmp, width, height, filter, cfg)
}

func resizeAndCropAtDepth(img image.Image, width, height int, anchor Anchor, filter ResampleFilter, cfg depthConfig) (image.Image, error) {
	srcW := img.Bounds().Dx()
	srcH := img.Bounds().Dy()
	srcAspectRatio := float64(srcW) / float64(srcH)
	dstAspectRatio := float64(width) / float64(height)

	var tmp image.Image
	var err error
	if srcAspectRatio < dstAspectRatio {
		tmp, err = resizeAtDepth(img, width, 0, filter, cfg)
	} else {
		tmp, err = resizeAtDepth(img, 0, height, filter, cfg)
	}
	if err != nil {
		return nil, err
	}
	return cropAtDepth(tmp, CropAnchorRect(tmp, width, height, anchor), cfg), nil
}

// ThumbnailWithDepth creates a thumbnail without dropping 10-bit input to
// 8-bit RGBA.
func ThumbnailWithDepth(img image.Image, width, height int, filter ResampleFilter, opts ...DepthOption) (image.Image, error) {
	cfg, err := applyDepthOptions(opts...)
	if err != nil {
		return nil, err
	}
	if err := cfg.begin("thumbnail", img); err != nil {
		cfg.logFail("thumbnail", err)
		return nil, err
	}
	dst, err := fillAtDepth(img, width, height, Center, filter, cfg)
	if err != nil {
		cfg.logFail("thumbnail", err)
		return nil, err
	}
	cfg.logExit("thumbnail")
	return dst, nil
}

// Thumbnail10 creates a 10-bit thumbnail.
func Thumbnail10(img image.Image, width, height int, filter ResampleFilter, opts ...DepthOption) (*image.NRGBA64, error) {
	opts = append([]DepthOption{WithBitDepth(BitDepth10)}, opts...)
	dst, err := ThumbnailWithDepth(img, width, height, filter, opts...)
	if err != nil {
		return nil, err
	}
	return dst.(*image.NRGBA64), nil
}

func newDepthImage(width, height int, depth BitDepth) image.Image {
	if width <= 0 || height <= 0 {
		if depth == BitDepth8 {
			return &image.NRGBA{}
		}
		return &image.NRGBA64{}
	}
	if depth == BitDepth8 {
		return image.NewNRGBA(image.Rect(0, 0, width, height))
	}
	return image.NewNRGBA64(image.Rect(0, 0, width, height))
}

func writeDepthPixel(dst image.Image, x, y int, r, g, b, a uint16, depth BitDepth, dither DitherMode) {
	switch out := dst.(type) {
	case *image.NRGBA64:
		writeNRGBA64(out, x, y, r, g, b, a, depth, dither)
	case *image.NRGBA:
		off := y*out.Stride + x*4
		out.Pix[off+0] = uint8(quantizeSample(r, BitDepth8, dither, x, y) >> 8)
		out.Pix[off+1] = uint8(quantizeSample(g, BitDepth8, dither, x, y) >> 8)
		out.Pix[off+2] = uint8(quantizeSample(b, BitDepth8, dither, x, y) >> 8)
		out.Pix[off+3] = uint8(quantizeSample(a, BitDepth8, DitherNone, x, y) >> 8)
	}
}

func clamp16(x float64) uint16 {
	v := int64(x + 0.5)
	if v > 0xffff {
		return 0xffff
	}
	if v > 0 {
		return uint16(v)
	}
	return 0
}
