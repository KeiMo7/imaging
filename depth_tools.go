package imaging

import (
	"encoding/binary"
	"image"
	"image/color"
	"math"
)

// CloneWithDepth copies img while preserving its bit depth, or converting to
// the depth selected by WithBitDepth.
func CloneWithDepth(img image.Image, opts ...DepthOption) (image.Image, error) {
	cfg, err := applyDepthOptions(opts...)
	if err != nil {
		return nil, err
	}
	if err := cfg.begin("clone", img); err != nil {
		cfg.logFail("clone", err)
		return nil, err
	}
	dst, err := cloneAtDepth(img, cfg.targetResolved, cfg.dither)
	if err != nil {
		cfg.logFail("clone", err)
		return nil, err
	}
	cfg.logExit("clone")
	return dst, nil
}

// Clone10 returns a 10-bit copy of img.
func Clone10(img image.Image, opts ...DepthOption) (*image.NRGBA64, error) {
	opts = append([]DepthOption{WithBitDepth(BitDepth10)}, opts...)
	dst, err := CloneWithDepth(img, opts...)
	if err != nil {
		return nil, err
	}
	return dst.(*image.NRGBA64), nil
}

// CropWithDepth cuts out rect without dropping high-bit-depth input.
func CropWithDepth(img image.Image, rect image.Rectangle, opts ...DepthOption) (image.Image, error) {
	cfg, err := applyDepthOptions(opts...)
	if err != nil {
		return nil, err
	}
	if err := cfg.begin("crop", img); err != nil {
		cfg.logFail("crop", err)
		return nil, err
	}
	dst := cropAtDepth(img, rect, cfg)
	cfg.logExit("crop")
	return dst, nil
}

// Crop10 cuts out rect into a 10-bit image.
func Crop10(img image.Image, rect image.Rectangle, opts ...DepthOption) (*image.NRGBA64, error) {
	opts = append([]DepthOption{WithBitDepth(BitDepth10)}, opts...)
	dst, err := CropWithDepth(img, rect, opts...)
	if err != nil {
		return nil, err
	}
	return dst.(*image.NRGBA64), nil
}

// CropAnchorWithDepth cuts out a rectangle of the requested size using anchor.
func CropAnchorWithDepth(img image.Image, width, height int, anchor Anchor, opts ...DepthOption) (image.Image, error) {
	return CropWithDepth(img, CropAnchorRect(img, width, height, anchor), opts...)
}

// CropCenterWithDepth cuts out a centered rectangle.
func CropCenterWithDepth(img image.Image, width, height int, opts ...DepthOption) (image.Image, error) {
	return CropAnchorWithDepth(img, width, height, Center, opts...)
}

// CropAnchorRect returns the absolute crop rectangle for the requested size
// and anchor.
func CropAnchorRect(img image.Image, width, height int, anchor Anchor) image.Rectangle {
	bounds := img.Bounds()
	pt := anchorPt(bounds, width, height, anchor)
	rect := image.Rect(0, 0, width, height).Add(pt)
	return bounds.Intersect(rect)
}

func cropAtDepth(img image.Image, rect image.Rectangle, cfg depthConfig) image.Image {
	bounds := img.Bounds()
	r := rect.Intersect(bounds).Sub(bounds.Min)
	if r.Empty() || r.Dx() <= 0 || r.Dy() <= 0 {
		return newDepthImage(0, 0, cfg.targetResolved)
	}
	if r.Eq(bounds.Sub(bounds.Min)) {
		dst, err := cloneAtDepth(img, cfg.targetResolved, cfg.dither)
		if err != nil {
			return newDepthImage(0, 0, cfg.targetResolved)
		}
		return dst
	}
	if cfg.targetResolved == BitDepth8 && cfg.input == BitDepth8 && cfg.dither == DitherNone {
		return Crop(img, rect)
	}

	src := newScanner64(img)
	dst := newDepthImage(r.Dx(), r.Dy(), cfg.targetResolved)
	row := make([]uint16, r.Dx()*4)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		src.scan(r.Min.X, y, r.Max.X, y+1, row)
		for x := 0; x < r.Dx(); x++ {
			i := x * 4
			writeDepthPixel(dst, x, y-r.Min.Y, row[i], row[i+1], row[i+2], row[i+3],
				cfg.targetResolved, cfg.dither)
		}
	}
	return dst
}

// FlipHWithDepth flips img horizontally while preserving its depth.
func FlipHWithDepth(img image.Image, opts ...DepthOption) (image.Image, error) {
	return transformWithDepth("flip-h", img, opts, func(dstX, dstY, w, h int) (int, int) {
		return w - dstX - 1, dstY
	})
}

// FlipVWithDepth flips img vertically while preserving its depth.
func FlipVWithDepth(img image.Image, opts ...DepthOption) (image.Image, error) {
	return transformWithDepth("flip-v", img, opts, func(dstX, dstY, w, h int) (int, int) {
		return dstX, h - dstY - 1
	})
}

// TransposeWithDepth transposes img while preserving its depth.
func TransposeWithDepth(img image.Image, opts ...DepthOption) (image.Image, error) {
	return transformWithDepth("transpose", img, opts, func(dstX, dstY, w, h int) (int, int) {
		return dstY, dstX
	})
}

// TransverseWithDepth applies a transverse transform while preserving depth.
func TransverseWithDepth(img image.Image, opts ...DepthOption) (image.Image, error) {
	return transformWithDepth("transverse", img, opts, func(dstX, dstY, w, h int) (int, int) {
		return h - dstY - 1, w - dstX - 1
	})
}

// Rotate90WithDepth rotates img 90 degrees counter-clockwise.
func Rotate90WithDepth(img image.Image, opts ...DepthOption) (image.Image, error) {
	return transformWithDepth("rotate-90", img, opts, func(dstX, dstY, w, h int) (int, int) {
		return h - dstY - 1, dstX
	})
}

// Rotate180WithDepth rotates img 180 degrees.
func Rotate180WithDepth(img image.Image, opts ...DepthOption) (image.Image, error) {
	return transformWithDepth("rotate-180", img, opts, func(dstX, dstY, w, h int) (int, int) {
		return w - dstX - 1, h - dstY - 1
	})
}

// Rotate270WithDepth rotates img 270 degrees counter-clockwise.
func Rotate270WithDepth(img image.Image, opts ...DepthOption) (image.Image, error) {
	return transformWithDepth("rotate-270", img, opts, func(dstX, dstY, w, h int) (int, int) {
		return dstY, w - dstX - 1
	})
}

func transformWithDepth(stage string, img image.Image, opts []DepthOption, mapPoint func(dstX, dstY, w, h int) (int, int)) (image.Image, error) {
	cfg, err := applyDepthOptions(opts...)
	if err != nil {
		return nil, err
	}
	if err := cfg.begin(stage, img); err != nil {
		cfg.logFail(stage, err)
		return nil, err
	}
	srcW := img.Bounds().Dx()
	srcH := img.Bounds().Dy()
	dstW, dstH := srcW, srcH
	switch stage {
	case "transpose", "transverse", "rotate-90", "rotate-270":
		dstW, dstH = srcH, srcW
	}
	src := newScanner64(img)
	dst := newDepthImage(dstW, dstH, cfg.targetResolved)
	var pixel [4]uint16
	for y := 0; y < dstH; y++ {
		for x := 0; x < dstW; x++ {
			sx, sy := mapPoint(x, y, srcW, srcH)
			src.scan(sx, sy, sx+1, sy+1, pixel[:])
			writeDepthPixel(dst, x, y, pixel[0], pixel[1], pixel[2], pixel[3],
				cfg.targetResolved, cfg.dither)
		}
	}
	cfg.logExit(stage)
	return dst, nil
}

// RotateWithDepth rotates img by angle degrees and preserves its depth.
func RotateWithDepth(img image.Image, angle float64, bgColor color.Color, opts ...DepthOption) (image.Image, error) {
	cfg, err := applyDepthOptions(opts...)
	if err != nil {
		return nil, err
	}
	if err := cfg.begin("rotate", img); err != nil {
		cfg.logFail("rotate", err)
		return nil, err
	}

	angle = angle - math.Floor(angle/360)*360
	switch angle {
	case 0:
		dst, err := cloneAtDepth(img, cfg.targetResolved, cfg.dither)
		if err != nil {
			cfg.logFail("rotate", err)
			return nil, err
		}
		cfg.logExit("rotate")
		return dst, nil
	case 90:
		dst, err := Rotate90WithDepth(img, opts...)
		if err != nil {
			cfg.logFail("rotate", err)
			return nil, err
		}
		cfg.logExit("rotate")
		return dst, nil
	case 180:
		dst, err := Rotate180WithDepth(img, opts...)
		if err != nil {
			cfg.logFail("rotate", err)
			return nil, err
		}
		cfg.logExit("rotate")
		return dst, nil
	case 270:
		dst, err := Rotate270WithDepth(img, opts...)
		if err != nil {
			cfg.logFail("rotate", err)
			return nil, err
		}
		cfg.logExit("rotate")
		return dst, nil
	}

	srcImage, err := cloneAtDepth(img, cfg.targetResolved, DitherNone)
	if err != nil {
		cfg.logFail("rotate", err)
		return nil, err
	}
	src := srcImage.(*image.NRGBA64)
	srcW := src.Bounds().Dx()
	srcH := src.Bounds().Dy()
	dstW, dstH := rotatedSize(srcW, srcH, angle)
	dst := newDepthImage(dstW, dstH, cfg.targetResolved)
	if dstW <= 0 || dstH <= 0 {
		cfg.logExit("rotate")
		return dst, nil
	}

	background := color.NRGBA64Model.Convert(bgColor).(color.NRGBA64)
	srcXOff := float64(srcW)/2 - 0.5
	srcYOff := float64(srcH)/2 - 0.5
	dstXOff := float64(dstW)/2 - 0.5
	dstYOff := float64(dstH)/2 - 0.5
	sin, cos := math.Sincos(math.Pi * angle / 180)

	parallel(0, dstH, func(ys <-chan int) {
		for dstY := range ys {
			for dstX := 0; dstX < dstW; dstX++ {
				xf, yf := rotatePoint(float64(dstX)-dstXOff, float64(dstY)-dstYOff, sin, cos)
				xf, yf = xf+srcXOff, yf+srcYOff
				r, g, b, a := interpolatePoint64(src, xf, yf, background)
				writeDepthPixel(dst, dstX, dstY, r, g, b, a, cfg.targetResolved, cfg.dither)
			}
		}
	})
	cfg.logExit("rotate")
	return dst, nil
}

func interpolatePoint64(src *image.NRGBA64, xf, yf float64, bgColor color.NRGBA64) (uint16, uint16, uint16, uint16) {
	x0 := int(math.Floor(xf))
	y0 := int(math.Floor(yf))
	bounds := src.Bounds()
	if !image.Pt(x0, y0).In(image.Rect(bounds.Min.X-1, bounds.Min.Y-1, bounds.Max.X, bounds.Max.Y)) {
		return bgColor.R, bgColor.G, bgColor.B, bgColor.A
	}

	xq := xf - float64(x0)
	yq := yf - float64(y0)
	points := [4]image.Point{
		{x0, y0},
		{x0 + 1, y0},
		{x0, y0 + 1},
		{x0 + 1, y0 + 1},
	}
	weights := [4]float64{
		(1 - xq) * (1 - yq),
		xq * (1 - yq),
		(1 - xq) * yq,
		xq * yq,
	}

	var r, g, b, a float64
	for i := 0; i < 4; i++ {
		p := points[i]
		w := weights[i]
		if p.In(bounds) {
			pixel := readNRGBA64Pixel(src, p.X, p.Y)
			wa := float64(pixel[3]) * w
			r += float64(pixel[0]) * wa
			g += float64(pixel[1]) * wa
			b += float64(pixel[2]) * wa
			a += wa
		} else {
			wa := float64(bgColor.A) * w
			r += float64(bgColor.R) * wa
			g += float64(bgColor.G) * wa
			b += float64(bgColor.B) * wa
			a += wa
		}
	}
	if a == 0 {
		return 0, 0, 0, 0
	}
	aInv := 1 / a
	return clamp16(r * aInv), clamp16(g * aInv), clamp16(b * aInv), clamp16(a)
}

// PasteWithDepth pastes img over background while preserving depth.
func PasteWithDepth(background, img image.Image, pos image.Point, opts ...DepthOption) (image.Image, error) {
	cfg, err := applyDepthOptions(opts...)
	if err != nil {
		return nil, err
	}
	if err := cfg.begin("paste", background); err != nil {
		cfg.logFail("paste", err)
		return nil, err
	}
	dst, err := cloneAtDepth(background, cfg.targetResolved, cfg.dither)
	if err != nil {
		cfg.logFail("paste", err)
		return nil, err
	}
	pos = pos.Sub(background.Bounds().Min)
	pasteRect := image.Rectangle{Min: pos, Max: pos.Add(img.Bounds().Size())}
	interRect := pasteRect.Intersect(dst.Bounds())
	if interRect.Empty() {
		cfg.logExit("paste")
		return dst, nil
	}

	src := newScanner64(img)
	row := make([]uint16, interRect.Dx()*4)
	for y := interRect.Min.Y; y < interRect.Max.Y; y++ {
		sx := interRect.Min.X - pasteRect.Min.X
		sy := y - pasteRect.Min.Y
		src.scan(sx, sy, sx+interRect.Dx(), sy+1, row)
		for x := 0; x < interRect.Dx(); x++ {
			i := x * 4
			writeDepthPixel(dst, interRect.Min.X+x, y, row[i], row[i+1], row[i+2], row[i+3],
				cfg.targetResolved, cfg.dither)
		}
	}
	cfg.logExit("paste")
	return dst, nil
}

// OverlayWithDepth blends img over background while preserving depth.
func OverlayWithDepth(background, img image.Image, pos image.Point, opacity float64, opts ...DepthOption) (image.Image, error) {
	opacity = math.Min(math.Max(opacity, 0), 1)
	cfg, err := applyDepthOptions(opts...)
	if err != nil {
		return nil, err
	}
	if err := cfg.begin("overlay", background); err != nil {
		cfg.logFail("overlay", err)
		return nil, err
	}
	dst, err := cloneAtDepth(background, cfg.targetResolved, cfg.dither)
	if err != nil {
		cfg.logFail("overlay", err)
		return nil, err
	}
	pos = pos.Sub(background.Bounds().Min)
	pasteRect := image.Rectangle{Min: pos, Max: pos.Add(img.Bounds().Size())}
	interRect := pasteRect.Intersect(dst.Bounds())
	if interRect.Empty() {
		cfg.logExit("overlay")
		return dst, nil
	}

	src := newScanner64(img)
	row := make([]uint16, interRect.Dx()*4)
	for y := interRect.Min.Y; y < interRect.Max.Y; y++ {
		sx := interRect.Min.X - pasteRect.Min.X
		sy := y - pasteRect.Min.Y
		src.scan(sx, sy, sx+interRect.Dx(), sy+1, row)

		var bgPixel [4]uint16
		for x := 0; x < interRect.Dx(); x++ {
			dx := interRect.Min.X + x
			readNRGBA64Sample(dst, dx, y, bgPixel[:])
			i := x * 4
			r1, g1, b1, a1 := float64(bgPixel[0]), float64(bgPixel[1]), float64(bgPixel[2]), float64(bgPixel[3])
			r2, g2, b2, a2 := float64(row[i]), float64(row[i+1]), float64(row[i+2]), float64(row[i+3])
			coef2 := opacity * a2 / 65535
			coef1 := (1 - coef2) * a1 / 65535
			coefSum := coef1 + coef2
			if coefSum == 0 {
				coefSum = 1
			}
			coef1 /= coefSum
			coef2 /= coefSum
			a := math.Min(a1+a2*opacity*(65535-a1)/65535, 65535)
			writeDepthPixel(dst, dx, y,
				clamp16(r1*coef1+r2*coef2),
				clamp16(g1*coef1+g2*coef2),
				clamp16(b1*coef1+b2*coef2),
				clamp16(a), cfg.targetResolved, cfg.dither)
		}
	}
	cfg.logExit("overlay")
	return dst, nil
}

func readNRGBA64Sample(img image.Image, x, y int, dst []uint16) {
	switch src := img.(type) {
	case *image.NRGBA64:
		pixel := readNRGBA64Pixel(src, x, y)
		copy(dst[:4], pixel[:])
	case *image.NRGBA:
		scanNRGBA(src, x, y, x+1, y+1, dst)
	default:
		newScanner64(img).scan(x, y, x+1, y+1, dst)
	}
}

func readNRGBA64Pixel(img *image.NRGBA64, x, y int) [4]uint16 {
	off := y*img.Stride + x*8
	p := img.Pix[off : off+8]
	return [4]uint16{
		binary.BigEndian.Uint16(p[0:2]),
		binary.BigEndian.Uint16(p[2:4]),
		binary.BigEndian.Uint16(p[4:6]),
		binary.BigEndian.Uint16(p[6:8]),
	}
}
