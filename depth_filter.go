package imaging

import (
	"image"
	"image/color"
	"math"
)

// GrayscaleWithDepth produces a grayscale image without dropping high bit
// depth input.
func GrayscaleWithDepth(img image.Image, opts ...DepthOption) (image.Image, error) {
	return mapPixelsWithDepth("grayscale", img, func(r, g, b, a uint16, x, y int) (uint16, uint16, uint16, uint16) {
		v := uint16(clamp16(0.299*float64(r) + 0.587*float64(g) + 0.114*float64(b)))
		return v, v, v, a
	}, opts...)
}

// InvertWithDepth inverts colors without dropping high bit depth input.
func InvertWithDepth(img image.Image, opts ...DepthOption) (image.Image, error) {
	return mapPixelsWithDepth("invert", img, func(r, g, b, a uint16, x, y int) (uint16, uint16, uint16, uint16) {
		return 0xffff - r, 0xffff - g, 0xffff - b, a
	}, opts...)
}

// AdjustFuncDepth applies fn to each non-premultiplied 16-bit RGBA pixel.
func AdjustFuncDepth(img image.Image, fn func(c color.NRGBA64) color.NRGBA64, opts ...DepthOption) (image.Image, error) {
	return mapPixelsWithDepth("adjust-func", img, func(r, g, b, a uint16, x, y int) (uint16, uint16, uint16, uint16) {
		c := fn(color.NRGBA64{R: r, G: g, B: b, A: a})
		return c.R, c.G, c.B, c.A
	}, opts...)
}

// AdjustContrastWithDepth applies contrast correction at the source depth.
func AdjustContrastWithDepth(img image.Image, percentage float64, opts ...DepthOption) (image.Image, error) {
	if percentage == 0 {
		return CloneWithDepth(img, opts...)
	}
	percentage = math.Min(math.Max(percentage, -100), 100)
	v := (100 + percentage) / 100
	return mapPixelsWithDepth("adjust-contrast", img, func(r, g, b, a uint16, x, y int) (uint16, uint16, uint16, uint16) {
		return adjustContrastSample(r, v), adjustContrastSample(g, v), adjustContrastSample(b, v), a
	}, opts...)
}

// AdjustBrightnessWithDepth applies brightness correction at the source depth.
func AdjustBrightnessWithDepth(img image.Image, percentage float64, opts ...DepthOption) (image.Image, error) {
	if percentage == 0 {
		return CloneWithDepth(img, opts...)
	}
	percentage = math.Min(math.Max(percentage, -100), 100)
	shift := 65535 * percentage / 100
	return mapPixelsWithDepth("adjust-brightness", img, func(r, g, b, a uint16, x, y int) (uint16, uint16, uint16, uint16) {
		return clamp16(float64(r) + shift), clamp16(float64(g) + shift), clamp16(float64(b) + shift), a
	}, opts...)
}

// AdjustGammaWithDepth applies gamma correction at the source depth.
func AdjustGammaWithDepth(img image.Image, gamma float64, opts ...DepthOption) (image.Image, error) {
	if gamma == 1 {
		return CloneWithDepth(img, opts...)
	}
	e := 1.0 / math.Max(gamma, 0.0001)
	lut := make([]uint16, 65536)
	for i := range lut {
		lut[i] = uint16(clamp16(math.Pow(float64(i)/65535, e) * 65535))
	}
	return mapLUTWithDepth("adjust-gamma", img, lut, opts...)
}

// AdjustSigmoidWithDepth applies sigmoid contrast at the source depth.
func AdjustSigmoidWithDepth(img image.Image, midpoint, factor float64, opts ...DepthOption) (image.Image, error) {
	if factor == 0 {
		return CloneWithDepth(img, opts...)
	}
	a := math.Min(math.Max(midpoint, 0), 1)
	b := math.Abs(factor)
	sig0 := sigmoid(a, b, 0)
	sig1 := sigmoid(a, b, 1)
	const epsilon = 1.0e-6
	lut := make([]uint16, 65536)
	for i := range lut {
		x := float64(i) / 65535
		if factor > 0 {
			f := (sigmoid(a, b, x) - sig0) / (sig1 - sig0)
			lut[i] = uint16(clamp16(f * 65535))
		} else {
			arg := math.Min(math.Max((sig1-sig0)*x+sig0, epsilon), 1-epsilon)
			f := a - math.Log(1/arg-1)/b
			lut[i] = uint16(clamp16(f * 65535))
		}
	}
	return mapLUTWithDepth("adjust-sigmoid", img, lut, opts...)
}

// AdjustSaturationWithDepth changes saturation without reducing the image to
// 8-bit samples.
func AdjustSaturationWithDepth(img image.Image, percentage float64, opts ...DepthOption) (image.Image, error) {
	if percentage == 0 {
		return CloneWithDepth(img, opts...)
	}
	percentage = math.Min(math.Max(percentage, -100), 100)
	multiplier := 1 + percentage/100
	return mapPixelsWithDepth("adjust-saturation", img, func(r, g, b, a uint16, x, y int) (uint16, uint16, uint16, uint16) {
		h, s, l := rgbToHSL16(r, g, b)
		s = math.Min(s*multiplier, 1)
		r, g, b = hslToRGB16(h, s, l)
		return r, g, b, a
	}, opts...)
}

// AdjustHueWithDepth changes hue without reducing the image to 8-bit samples.
func AdjustHueWithDepth(img image.Image, shift float64, opts ...DepthOption) (image.Image, error) {
	if math.Mod(shift, 360) == 0 {
		return CloneWithDepth(img, opts...)
	}
	summand := shift / 360
	return mapPixelsWithDepth("adjust-hue", img, func(r, g, b, a uint16, x, y int) (uint16, uint16, uint16, uint16) {
		h, s, l := rgbToHSL16(r, g, b)
		h = math.Mod(h+summand, 1)
		if h < 0 {
			h++
		}
		r, g, b = hslToRGB16(h, s, l)
		return r, g, b, a
	}, opts...)
}

func mapLUTWithDepth(stage string, img image.Image, lut []uint16, opts ...DepthOption) (image.Image, error) {
	return mapPixelsWithDepth(stage, img, func(r, g, b, a uint16, x, y int) (uint16, uint16, uint16, uint16) {
		return lut[r], lut[g], lut[b], a
	}, opts...)
}

func mapPixelsWithDepth(stage string, img image.Image, fn func(r, g, b, a uint16, x, y int) (uint16, uint16, uint16, uint16), opts ...DepthOption) (image.Image, error) {
	cfg, err := applyDepthOptions(opts...)
	if err != nil {
		return nil, err
	}
	if err := cfg.begin(stage, img); err != nil {
		cfg.logFail(stage, err)
		return nil, err
	}

	src := newScanner64(img)
	dst := newDepthImage(src.w, src.h, cfg.targetResolved)
	parallel(0, src.h, func(ys <-chan int) {
		row := make([]uint16, src.w*4)
		for y := range ys {
			src.scan(0, y, src.w, y+1, row)
			for x := 0; x < src.w; x++ {
				i := x * 4
				r, g, b, a := fn(row[i], row[i+1], row[i+2], row[i+3], x, y)
				writeDepthPixel(dst, x, y, r, g, b, a, cfg.targetResolved, cfg.dither)
			}
		}
	})
	cfg.logExit(stage)
	return dst, nil
}

func adjustContrastSample(v uint16, factor float64) uint16 {
	x := float64(v) / 65535
	switch {
	case factor >= 0 && factor <= 1:
		return uint16(clamp16((0.5 + (x-0.5)*factor) * 65535))
	case factor > 1 && factor < 2:
		return uint16(clamp16((0.5 + (x-0.5)*(1/(2-factor))) * 65535))
	default:
		return uint16(clamp16(x * 65535))
	}
}

func rgbToHSL16(r, g, b uint16) (float64, float64, float64) {
	rr := float64(r) / 65535
	gg := float64(g) / 65535
	bb := float64(b) / 65535
	max := math.Max(rr, math.Max(gg, bb))
	min := math.Min(rr, math.Min(gg, bb))
	l := (max + min) / 2
	if max == min {
		return 0, 0, l
	}

	d := max - min
	var s float64
	if l > 0.5 {
		s = d / (2 - max - min)
	} else {
		s = d / (max + min)
	}

	var h float64
	switch max {
	case rr:
		h = (gg - bb) / d
		if g < b {
			h += 6
		}
	case gg:
		h = (bb-rr)/d + 2
	case bb:
		h = (rr-gg)/d + 4
	}
	return h / 6, s, l
}

func hslToRGB16(h, s, l float64) (uint16, uint16, uint16) {
	if s == 0 {
		v := uint16(clamp16(l * 65535))
		return v, v, v
	}
	var q float64
	if l < 0.5 {
		q = l * (1 + s)
	} else {
		q = l + s - l*s
	}
	p := 2*l - q
	r := hueToRGB(p, q, h+1/3.0)
	g := hueToRGB(p, q, h)
	b := hueToRGB(p, q, h-1/3.0)
	return uint16(clamp16(r * 65535)), uint16(clamp16(g * 65535)), uint16(clamp16(b * 65535))
}

// BlurWithDepth blurs img without reducing it to 8-bit samples.
func BlurWithDepth(img image.Image, sigma float64, opts ...DepthOption) (image.Image, error) {
	if sigma <= 0 {
		return CloneWithDepth(img, opts...)
	}
	cfg, err := applyDepthOptions(opts...)
	if err != nil {
		return nil, err
	}
	if err := cfg.begin("blur", img); err != nil {
		cfg.logFail("blur", err)
		return nil, err
	}
	radius := int(math.Ceil(sigma * 3.0))
	kernel := make([]float64, radius+1)
	for i := 0; i <= radius; i++ {
		kernel[i] = gaussianBlurKernel(float64(i), sigma)
	}
	tmp := blurHorizontalAtDepth(img, kernel, cfg)
	dst := blurVerticalAtDepth(tmp, kernel, cfg)
	cfg.logExit("blur")
	return dst, nil
}

func blurHorizontalAtDepth(img image.Image, kernel []float64, cfg depthConfig) image.Image {
	src := newScanner64(img)
	dst := newDepthImage(src.w, src.h, cfg.targetResolved)
	radius := len(kernel) - 1
	parallel(0, src.h, func(ys <-chan int) {
		scanLine := make([]uint16, src.w*4)
		scanLineF := make([]float64, len(scanLine))
		for y := range ys {
			src.scan(0, y, src.w, y+1, scanLine)
			for i, v := range scanLine {
				scanLineF[i] = float64(v)
			}
			for x := 0; x < src.w; x++ {
				min := maxInt(x-radius, 0)
				max := minInt(x+radius, src.w-1)
				var r, g, b, a, wsum float64
				for ix := min; ix <= max; ix++ {
					i := ix * 4
					weight := kernel[absint(x-ix)]
					wsum += weight
					s := scanLineF[i : i+4 : i+4]
					wa := s[3] * weight
					r += s[0] * wa
					g += s[1] * wa
					b += s[2] * wa
					a += wa
				}
				if a != 0 {
					aInv := 1 / a
					writeDepthPixel(dst, x, y,
						clamp16(r*aInv), clamp16(g*aInv), clamp16(b*aInv), clamp16(a/wsum),
						cfg.targetResolved, cfg.dither)
				}
			}
		}
	})
	return dst
}

func blurVerticalAtDepth(img image.Image, kernel []float64, cfg depthConfig) image.Image {
	src := newScanner64(img)
	dst := newDepthImage(src.w, src.h, cfg.targetResolved)
	radius := len(kernel) - 1
	parallel(0, src.w, func(xs <-chan int) {
		scanLine := make([]uint16, src.h*4)
		scanLineF := make([]float64, len(scanLine))
		for x := range xs {
			src.scan(x, 0, x+1, src.h, scanLine)
			for i, v := range scanLine {
				scanLineF[i] = float64(v)
			}
			for y := 0; y < src.h; y++ {
				min := maxInt(y-radius, 0)
				max := minInt(y+radius, src.h-1)
				var r, g, b, a, wsum float64
				for iy := min; iy <= max; iy++ {
					i := iy * 4
					weight := kernel[absint(y-iy)]
					wsum += weight
					s := scanLineF[i : i+4 : i+4]
					wa := s[3] * weight
					r += s[0] * wa
					g += s[1] * wa
					b += s[2] * wa
					a += wa
				}
				if a != 0 {
					aInv := 1 / a
					writeDepthPixel(dst, x, y,
						clamp16(r*aInv), clamp16(g*aInv), clamp16(b*aInv), clamp16(a/wsum),
						cfg.targetResolved, cfg.dither)
				}
			}
		}
	})
	return dst
}

// SharpenWithDepth sharpens img without reducing it to 8-bit samples.
func SharpenWithDepth(img image.Image, sigma float64, opts ...DepthOption) (image.Image, error) {
	if sigma <= 0 {
		return CloneWithDepth(img, opts...)
	}
	cfg, err := applyDepthOptions(opts...)
	if err != nil {
		return nil, err
	}
	if err := cfg.begin("sharpen", img); err != nil {
		cfg.logFail("sharpen", err)
		return nil, err
	}
	blurred, err := BlurWithDepth(img, sigma, WithBitDepth(cfg.targetResolved), WithDither(cfg.dither))
	if err != nil {
		cfg.logFail("sharpen", err)
		return nil, err
	}
	src := newScanner64(img)
	dst := newDepthImage(src.w, src.h, cfg.targetResolved)
	blurredScanner := newScanner64(blurred)
	parallel(0, src.h, func(ys <-chan int) {
		row := make([]uint16, src.w*4)
		blurredRow := make([]uint16, src.w*4)
		for y := range ys {
			src.scan(0, y, src.w, y+1, row)
			blurredScanner.scan(0, y, src.w, y+1, blurredRow)
			for x := 0; x < src.w; x++ {
				i := x * 4
				writeDepthPixel(dst, x, y,
					clamp16(float64(row[i])*2-float64(blurredRow[i])),
					clamp16(float64(row[i+1])*2-float64(blurredRow[i+1])),
					clamp16(float64(row[i+2])*2-float64(blurredRow[i+2])),
					row[i+3], cfg.targetResolved, cfg.dither)
			}
		}
	})
	cfg.logExit("sharpen")
	return dst, nil
}

// Convolve3x3WithDepth convolves img without reducing it to 8-bit samples.
func Convolve3x3WithDepth(img image.Image, kernel [9]float64, options *ConvolveOptions, opts ...DepthOption) (image.Image, error) {
	return convolveWithDepth(img, kernel[:], options, opts...)
}

// Convolve5x5WithDepth convolves img without reducing it to 8-bit samples.
func Convolve5x5WithDepth(img image.Image, kernel [25]float64, options *ConvolveOptions, opts ...DepthOption) (image.Image, error) {
	return convolveWithDepth(img, kernel[:], options, opts...)
}

func convolveWithDepth(img image.Image, kernel []float64, options *ConvolveOptions, opts ...DepthOption) (image.Image, error) {
	cfg, err := applyDepthOptions(opts...)
	if err != nil {
		return nil, err
	}
	if err := cfg.begin("convolve", img); err != nil {
		cfg.logFail("convolve", err)
		return nil, err
	}
	src := newScanner64(img)
	dst := newDepthImage(src.w, src.h, cfg.targetResolved)
	if src.w < 1 || src.h < 1 {
		cfg.logExit("convolve")
		return dst, nil
	}
	if options == nil {
		options = &ConvolveOptions{}
	}
	if options.Normalize {
		normalizeKernel(kernel)
	}
	type coef struct {
		x, y int
		k    float64
	}
	coefs := make([]coef, 0, len(kernel))
	m := 1
	if len(kernel) == 25 {
		m = 2
	}
	i := 0
	for y := -m; y <= m; y++ {
		for x := -m; x <= m; x++ {
			if kernel[i] != 0 {
				coefs = append(coefs, coef{x: x, y: y, k: kernel[i]})
			}
			i++
		}
	}

	parallel(0, src.h, func(ys <-chan int) {
		row := make([]uint16, src.w*4)
		for y := range ys {
			src.scan(0, y, src.w, y+1, row)
			for x := 0; x < src.w; x++ {
				var r, g, b float64
				for _, c := range coefs {
					ix := minInt(maxInt(x+c.x, 0), src.w-1)
					iy := minInt(maxInt(y+c.y, 0), src.h-1)
					var sample [4]uint16
					src.scan(ix, iy, ix+1, iy+1, sample[:])
					r += float64(sample[0]) * c.k
					g += float64(sample[1]) * c.k
					b += float64(sample[2]) * c.k
				}
				if options.Abs {
					r = math.Abs(r)
					g = math.Abs(g)
					b = math.Abs(b)
				}
				if options.Bias != 0 {
					bias := float64(options.Bias) * 257
					r += bias
					g += bias
					b += bias
				}
				i := x * 4
				writeDepthPixel(dst, x, y, clamp16(r), clamp16(g), clamp16(b), row[i+3],
					cfg.targetResolved, cfg.dither)
			}
		}
	})
	cfg.logExit("convolve")
	return dst, nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
