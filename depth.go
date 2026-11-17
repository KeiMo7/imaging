package imaging

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
)

// BitDepth identifies the number of significant bits per color channel.
//
// BitDepthAuto selects a depth from the input image or the declared input
// depth. The zero value is BitDepthAuto, which keeps the existing 8-bit APIs
// unchanged when depth options are omitted.
type BitDepth uint8

// Supported bit depths.
const (
	BitDepthAuto BitDepth = 0
	BitDepth8    BitDepth = 8
	BitDepth10   BitDepth = 10
	BitDepth16   BitDepth = 16
)

func (d BitDepth) String() string {
	switch d {
	case BitDepthAuto:
		return "auto"
	case BitDepth8:
		return "8"
	case BitDepth10:
		return "10"
	case BitDepth16:
		return "16"
	default:
		return fmt.Sprintf("invalid(%d)", d)
	}
}

func (d BitDepth) validTarget() bool {
	return d == BitDepthAuto || d == BitDepth8 || d == BitDepth10 || d == BitDepth16
}

// DitherMode controls quantization noise used while converting bit depths.
type DitherMode uint8

// Supported dither modes. DitherNone is the default and keeps 8-bit outputs
// bit-for-bit compatible with the original processing path.
const (
	DitherNone DitherMode = iota
	DitherOrdered
)

func (d DitherMode) String() string {
	switch d {
	case DitherNone:
		return "none"
	case DitherOrdered:
		return "ordered"
	default:
		return fmt.Sprintf("invalid(%d)", d)
	}
}

// BitDepthLogLevel is the severity of a bit-depth decision log entry.
type BitDepthLogLevel uint8

// Bit-depth log levels follow the usual debug/info/warning/error ordering.
const (
	BitDepthLogDebug BitDepthLogLevel = iota
	BitDepthLogInfo
	BitDepthLogWarn
	BitDepthLogError
)

func (l BitDepthLogLevel) String() string {
	switch l {
	case BitDepthLogDebug:
		return "debug"
	case BitDepthLogInfo:
		return "info"
	case BitDepthLogWarn:
		return "warn"
	case BitDepthLogError:
		return "error"
	default:
		return "unknown"
	}
}

// BitDepthLogger receives bit-depth decisions. A nil logger is a no-op, so the
// default behavior does not emit logs.
type BitDepthLogger func(level BitDepthLogLevel, format string, args ...interface{})

var (
	// ErrUnsupportedBitDepth means that the requested bit depth cannot be
	// produced by the operation or output format.
	ErrUnsupportedBitDepth = errors.New("imaging: unsupported bit depth")

	// ErrBitDepthMismatch means that the declared input depth does not match
	// the sample values.
	ErrBitDepthMismatch = errors.New("imaging: declared bit depth does not match pixel data")

	// ErrUnsupportedPixelFormat means that a raw pixel format conversion is not
	// supported.
	ErrUnsupportedPixelFormat = errors.New("imaging: unsupported pixel format")
)

type depthConfig struct {
	target         BitDepth
	declared       BitDepth
	dither         DitherMode
	logger         BitDepthLogger
	input          BitDepth
	targetResolved BitDepth
}

// DepthOption sets an optional parameter for the *WithDepth processing
// functions.
type DepthOption func(*depthConfig)

// WithBitDepth sets the output bit depth. Omit this option to preserve the
// input depth.
func WithBitDepth(depth BitDepth) DepthOption {
	return func(c *depthConfig) {
		c.target = depth
	}
}

// WithDither sets the dither mode used when changing bit depth. The default is
// DitherNone.
func WithDither(mode DitherMode) DepthOption {
	return func(c *depthConfig) {
		c.dither = mode
	}
}

// WithDeclaredBitDepth declares the input bit depth. If the pixel samples do
// not fit that depth, processing fails with ErrBitDepthMismatch.
func WithDeclaredBitDepth(depth BitDepth) DepthOption {
	return func(c *depthConfig) {
		c.declared = depth
	}
}

// WithBitDepthLogger enables bit-depth entry, exit, and failure logs.
func WithBitDepthLogger(logger BitDepthLogger) DepthOption {
	return func(c *depthConfig) {
		c.logger = logger
	}
}

func applyDepthOptions(opts ...DepthOption) (depthConfig, error) {
	cfg := depthConfig{
		target: BitDepthAuto,
		dither: DitherNone,
	}
	for _, option := range opts {
		if option != nil {
			option(&cfg)
		}
	}
	if !cfg.target.validTarget() {
		return cfg, fmt.Errorf("%w: target %s", ErrUnsupportedBitDepth, cfg.target)
	}
	if !cfg.declared.validTarget() {
		return cfg, fmt.Errorf("%w: declared %s", ErrUnsupportedBitDepth, cfg.declared)
	}
	if cfg.dither != DitherNone && cfg.dither != DitherOrdered {
		return cfg, fmt.Errorf("%w: dither mode %d", ErrUnsupportedBitDepth, cfg.dither)
	}
	return cfg, nil
}

func (c *depthConfig) log(level BitDepthLogLevel, format string, args ...interface{}) {
	if c.logger != nil {
		c.logger(level, format, args...)
	}
}

func (c *depthConfig) logEnter(stage string) {
	c.log(BitDepthLogDebug, "imaging: bitdepth enter stage=%s input=%s target=%s declared=%s dither=%s",
		stage, c.input, c.target, c.declared, c.dither)
}

func (c *depthConfig) logExit(stage string) {
	c.log(BitDepthLogDebug, "imaging: bitdepth exit stage=%s output=%s dither=%s",
		stage, c.targetResolved, c.dither)
}

func (c *depthConfig) logFail(stage string, err error) {
	c.log(BitDepthLogError, "imaging: bitdepth fail stage=%s input=%s target=%s error=%v",
		stage, c.input, c.target, err)
}

func (c *depthConfig) begin(stage string, img image.Image) error {
	if err := c.prepare(img); err != nil {
		c.logEnter(stage)
		return err
	}
	c.logEnter(stage)
	return c.finish(img)
}

func (c *depthConfig) resolve(img image.Image) error {
	if err := c.prepare(img); err != nil {
		return err
	}
	return c.finish(img)
}

func (c *depthConfig) prepare(img image.Image) error {
	if img == nil {
		return errors.New("imaging: nil image")
	}

	if c.declared != BitDepthAuto {
		c.input = c.declared
	} else {
		c.input = DetectBitDepth(img)
	}
	return nil
}

func (c *depthConfig) finish(img image.Image) error {
	if c.declared != BitDepthAuto {
		if err := validateDeclaredDepth(img, c.declared); err != nil {
			return err
		}
	}

	if c.target == BitDepthAuto {
		c.targetResolved = c.input
	} else {
		c.targetResolved = c.target
	}
	return nil
}

// DepthProvider can expose exact depth metadata that cannot be inferred from
// an image's concrete Go type.
type DepthProvider interface {
	Depth() BitDepth
}

// DetectBitDepth returns the effective bit depth of img.
//
// Eight-bit image types are reported directly. For 16-bit containers, the
// samples are checked for 8-bit replication or 10-bit alignment. Callers that
// know the source depth should use WithDeclaredBitDepth instead of relying on
// this heuristic.
func DetectBitDepth(img image.Image) BitDepth {
	if img == nil {
		return BitDepthAuto
	}
	if provider, ok := img.(DepthProvider); ok {
		depth := provider.Depth()
		if depth == BitDepth8 || depth == BitDepth10 || depth == BitDepth16 {
			return depth
		}
	}

	switch img.(type) {
	case *image.NRGBA, *image.RGBA, *image.Gray, *image.Alpha,
		*image.YCbCr, *image.Paletted:
		return BitDepth8
	case *image.NRGBA64, *image.RGBA64:
	default:
		if img.Bounds().Empty() {
			return BitDepth8
		}
		r, g, b, a := img.At(img.Bounds().Min.X, img.Bounds().Min.Y).RGBA()
		if r <= 0xff && g <= 0xff && b <= 0xff && a <= 0xff {
			return BitDepth8
		}
	}

	src := newScanner64(img)
	all8 := true
	all10 := true
	row := make([]uint16, src.w*4)
	for y := 0; y < src.h && (all8 || all10); y++ {
		src.scan(0, y, src.w, y+1, row)
		for _, v := range row {
			if all8 && !is8BitSample(v) {
				all8 = false
			}
			if all10 && !is10BitSample(v) {
				all10 = false
			}
			if !all8 && !all10 {
				break
			}
		}
	}
	if all8 {
		return BitDepth8
	}
	if all10 {
		return BitDepth10
	}
	return BitDepth16
}

func validateDeclaredDepth(img image.Image, declared BitDepth) error {
	if declared == BitDepthAuto || declared == BitDepth16 {
		return nil
	}

	src := newScanner64(img)
	row := make([]uint16, src.w*4)
	for y := 0; y < src.h; y++ {
		src.scan(0, y, src.w, y+1, row)
		for _, v := range row {
			ok := false
			switch declared {
			case BitDepth8:
				ok = is8BitSample(v)
			case BitDepth10:
				ok = is10BitSample(v)
			}
			if !ok {
				return fmt.Errorf("%w: declared %s-bit input contains sample 0x%04x at row %d",
					ErrBitDepthMismatch, declared, v, y)
			}
		}
	}
	return nil
}

func is8BitSample(v uint16) bool {
	return v <= 0xff || v == expand8(uint8(v>>8))
}

func is10BitSample(v uint16) bool {
	if v <= 0x3ff {
		return true
	}
	q := uint16(v >> 6)
	return v == q<<6 || v == expand10(q)
}

// ConvertToDepth converts img to the configured bit depth without passing
// through an 8-bit intermediate for 10-bit and 16-bit targets.
func ConvertToDepth(img image.Image, opts ...DepthOption) (image.Image, error) {
	cfg, err := applyDepthOptions(opts...)
	if err != nil {
		return nil, err
	}
	if err := cfg.begin("convert", img); err != nil {
		cfg.logFail("convert", err)
		return nil, err
	}
	dst, err := cloneAtDepth(img, cfg.targetResolved, cfg.dither)
	if err != nil {
		cfg.logFail("convert", err)
		return nil, err
	}
	cfg.logExit("convert")
	return dst, nil
}

// ConvertTo10 converts img to a 10-bit image stored in an NRGBA64 container.
func ConvertTo10(img image.Image, opts ...DepthOption) (*image.NRGBA64, error) {
	opts = append([]DepthOption{WithBitDepth(BitDepth10)}, opts...)
	dst, err := ConvertToDepth(img, opts...)
	if err != nil {
		return nil, err
	}
	return dst.(*image.NRGBA64), nil
}

func cloneAtDepth(img image.Image, depth BitDepth, dither DitherMode) (image.Image, error) {
	switch depth {
	case BitDepth8:
		if dither != DitherNone {
			src := newScanner64(img)
			dst := image.NewNRGBA(image.Rect(0, 0, src.w, src.h))
			row := make([]uint16, src.w*4)
			for y := 0; y < src.h; y++ {
				src.scan(0, y, src.w, y+1, row)
				for x := 0; x < src.w; x++ {
					i := x * 4
					writeDepthPixel(dst, x, y, row[i], row[i+1], row[i+2], row[i+3], depth, dither)
				}
			}
			return dst, nil
		}
		return Clone(img), nil
	case BitDepth10:
		if src, ok := img.(*image.NRGBA64); ok && DetectBitDepth(src) == BitDepth10 {
			return cloneNRGBA64Exact(src), nil
		}
		fallthrough
	case BitDepth16:
		if src, ok := img.(*image.NRGBA64); ok && depth == BitDepth16 {
			return cloneNRGBA64Exact(src), nil
		}
		src := newScanner64(img)
		dst := image.NewNRGBA64(image.Rect(0, 0, src.w, src.h))
		row := make([]uint16, src.w*4)
		for y := 0; y < src.h; y++ {
			src.scan(0, y, src.w, y+1, row)
			for x := 0; x < src.w; x++ {
				i := x * 4
				writeNRGBA64(dst, x, y, row[i], row[i+1], row[i+2], row[i+3], depth, dither)
			}
		}
		return dst, nil
	default:
		return nil, fmt.Errorf("%w: target %s", ErrUnsupportedBitDepth, depth)
	}
}

func cloneNRGBA64Exact(src *image.NRGBA64) *image.NRGBA64 {
	bounds := src.Bounds()
	dst := image.NewNRGBA64(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	for y := 0; y < bounds.Dy(); y++ {
		srcOff := (y+bounds.Min.Y-src.Rect.Min.Y)*src.Stride + (bounds.Min.X-src.Rect.Min.X)*8
		copy(
			dst.Pix[y*dst.Stride:y*dst.Stride+bounds.Dx()*8],
			src.Pix[srcOff:srcOff+bounds.Dx()*8],
		)
	}
	return dst
}

func writeNRGBA64(dst *image.NRGBA64, x, y int, r, g, b, a uint16, depth BitDepth, dither DitherMode) {
	off := y*dst.Stride + x*8
	p := dst.Pix[off : off+8]
	binary.BigEndian.PutUint16(p[0:2], quantizeSample(r, depth, dither, x, y))
	binary.BigEndian.PutUint16(p[2:4], quantizeSample(g, depth, dither, x, y))
	binary.BigEndian.PutUint16(p[4:6], quantizeSample(b, depth, dither, x, y))
	binary.BigEndian.PutUint16(p[6:8], quantizeSample(a, depth, DitherNone, x, y))
}

func quantizeSample(v uint16, depth BitDepth, dither DitherMode, x, y int) uint16 {
	switch depth {
	case BitDepth8:
		q := float64(v)*255/65535 + ditherOffset(dither, x, y)
		return uint16(clampDepth(q, 255)) * 0x101
	case BitDepth10:
		q := float64(v)*1023/65535 + ditherOffset(dither, x, y)
		return expand10(uint16(clampDepth(q, 1023)))
	case BitDepth16:
		return v
	default:
		return v
	}
}

func clampDepth(v float64, max int) int64 {
	iv := int64(v + 0.5)
	if iv < 0 {
		return 0
	}
	if iv > int64(max) {
		return int64(max)
	}
	return iv
}

func ditherOffset(mode DitherMode, x, y int) float64 {
	if mode != DitherOrdered {
		return 0
	}
	return (bayer4x4[(y&3)*4+(x&3)]+0.5)/16 - 0.5
}

var bayer4x4 = [16]float64{
	0, 8, 2, 10,
	12, 4, 14, 6,
	3, 11, 1, 9,
	15, 7, 13, 5,
}

func expand8(v uint8) uint16 {
	return uint16(v) * 0x101
}

func expand10(v uint16) uint16 {
	v &= 0x3ff
	return v<<6 | v>>4
}

func scanNRGBA64(img *image.NRGBA64, x1, y1, x2, y2 int, dst []uint16) {
	j := 0
	for y := y1; y < y2; y++ {
		i := y*img.Stride + x1*8
		for x := x1; x < x2; x++ {
			dst[j+0] = binary.BigEndian.Uint16(img.Pix[i+0 : i+2])
			dst[j+1] = binary.BigEndian.Uint16(img.Pix[i+2 : i+4])
			dst[j+2] = binary.BigEndian.Uint16(img.Pix[i+4 : i+6])
			dst[j+3] = binary.BigEndian.Uint16(img.Pix[i+6 : i+8])
			j += 4
			i += 8
		}
	}
}

func scanRGBA64(img *image.RGBA64, x1, y1, x2, y2 int, dst []uint16) {
	j := 0
	for y := y1; y < y2; y++ {
		i := y*img.Stride + x1*8
		for x := x1; x < x2; x++ {
			r := binary.BigEndian.Uint16(img.Pix[i+0 : i+2])
			g := binary.BigEndian.Uint16(img.Pix[i+2 : i+4])
			b := binary.BigEndian.Uint16(img.Pix[i+4 : i+6])
			a := binary.BigEndian.Uint16(img.Pix[i+6 : i+8])
			switch a {
			case 0:
				r, g, b = 0, 0, 0
			case 0xffff:
			default:
				r = uint16(uint32(r) * 0xffff / uint32(a))
				g = uint16(uint32(g) * 0xffff / uint32(a))
				b = uint16(uint32(b) * 0xffff / uint32(a))
			}
			dst[j+0] = r
			dst[j+1] = g
			dst[j+2] = b
			dst[j+3] = a
			j += 4
			i += 8
		}
	}
}

func scanNRGBA(img *image.NRGBA, x1, y1, x2, y2 int, dst []uint16) {
	j := 0
	for y := y1; y < y2; y++ {
		i := y*img.Stride + x1*4
		for x := x1; x < x2; x++ {
			dst[j+0] = expand8(img.Pix[i+0])
			dst[j+1] = expand8(img.Pix[i+1])
			dst[j+2] = expand8(img.Pix[i+2])
			dst[j+3] = expand8(img.Pix[i+3])
			j += 4
			i += 4
		}
	}
}

func scanRGBA(img *image.RGBA, x1, y1, x2, y2 int, dst []uint16) {
	j := 0
	for y := y1; y < y2; y++ {
		i := y*img.Stride + x1*4
		for x := x1; x < x2; x++ {
			r := img.Pix[i+0]
			g := img.Pix[i+1]
			b := img.Pix[i+2]
			a := img.Pix[i+3]
			switch a {
			case 0:
				r, g, b = 0, 0, 0
			case 0xff:
			default:
				r = uint8(uint16(r) * 0xff / uint16(a))
				g = uint8(uint16(g) * 0xff / uint16(a))
				b = uint8(uint16(b) * 0xff / uint16(a))
			}
			dst[j+0] = expand8(r)
			dst[j+1] = expand8(g)
			dst[j+2] = expand8(b)
			dst[j+3] = expand8(a)
			j += 4
			i += 4
		}
	}
}

// scanner64 is the 16-bit counterpart of scanner. It always produces
// non-premultiplied RGBA samples in the range 0..65535.
type scanner64 struct {
	image image.Image
	w, h  int
}

func newScanner64(img image.Image) *scanner64 {
	return &scanner64{
		image: img,
		w:     img.Bounds().Dx(),
		h:     img.Bounds().Dy(),
	}
}

func (s *scanner64) scan(x1, y1, x2, y2 int, dst []uint16) {
	switch img := s.image.(type) {
	case *image.NRGBA64:
		scanNRGBA64(img, x1, y1, x2, y2, dst)
		return
	case *image.RGBA64:
		scanRGBA64(img, x1, y1, x2, y2, dst)
		return
	case *image.NRGBA:
		scanNRGBA(img, x1, y1, x2, y2, dst)
		return
	case *image.RGBA:
		scanRGBA(img, x1, y1, x2, y2, dst)
		return
	case *image.Gray:
		j := 0
		for y := y1; y < y2; y++ {
			i := y*img.Stride + x1
			for x := x1; x < x2; x++ {
				v := expand8(img.Pix[i])
				dst[j+0], dst[j+1], dst[j+2], dst[j+3] = v, v, v, 0xffff
				j += 4
				i++
			}
		}
		return
	case *image.Gray16:
		j := 0
		for y := y1; y < y2; y++ {
			i := y*img.Stride + x1*2
			for x := x1; x < x2; x++ {
				v := binary.BigEndian.Uint16(img.Pix[i : i+2])
				dst[j+0], dst[j+1], dst[j+2], dst[j+3] = v, v, v, 0xffff
				j += 4
				i += 2
			}
		}
		return
	case *image.Alpha:
		j := 0
		for y := y1; y < y2; y++ {
			i := y*img.Stride + x1
			for x := x1; x < x2; x++ {
				dst[j+0], dst[j+1], dst[j+2], dst[j+3] = 0, 0, 0, expand8(img.Pix[i])
				j += 4
				i++
			}
		}
		return
	case *image.Alpha16:
		j := 0
		for y := y1; y < y2; y++ {
			i := y*img.Stride + x1*2
			for x := x1; x < x2; x++ {
				dst[j+0], dst[j+1], dst[j+2], dst[j+3] = 0, 0, 0, binary.BigEndian.Uint16(img.Pix[i:i+2])
				j += 4
				i += 2
			}
		}
		return
	case *image.YCbCr:
		j := 0
		b := img.Bounds()
		for y := y1 + b.Min.Y; y < y2+b.Min.Y; y++ {
			for x := x1 + b.Min.X; x < x2+b.Min.X; x++ {
				yi := img.YOffset(x, y)
				ci := img.COffset(x, y)
				r, g, bl := color.YCbCrToRGB(img.Y[yi], img.Cb[ci], img.Cr[ci])
				dst[j+0] = expand8(r)
				dst[j+1] = expand8(g)
				dst[j+2] = expand8(bl)
				dst[j+3] = 0xffff
				j += 4
			}
		}
		return
	case *YUV420P10LE:
		img.scanRGB(x1, y1, x2, y2, dst)
		return
	case *image.Paletted:
		j := 0
		for y := y1; y < y2; y++ {
			i := y*img.Stride + x1
			for x := x1; x < x2; x++ {
				r, g, b, a := img.Palette[img.Pix[i]].RGBA()
				if a != 0 && a != 0xffff {
					r = r * 0xffff / a
					g = g * 0xffff / a
					b = b * 0xffff / a
				}
				dst[j+0] = uint16(r)
				dst[j+1] = uint16(g)
				dst[j+2] = uint16(b)
				dst[j+3] = uint16(a)
				j += 4
				i++
			}
		}
		return
	}

	j := 0
	b := s.image.Bounds()
	for y := y1 + b.Min.Y; y < y2+b.Min.Y; y++ {
		for x := x1 + b.Min.X; x < x2+b.Min.X; x++ {
			r, g, bl, a := s.image.At(x, y).RGBA()
			if a != 0 && a != 0xffff {
				r = r * 0xffff / a
				g = g * 0xffff / a
				bl = bl * 0xffff / a
			}
			dst[j+0] = uint16(r)
			dst[j+1] = uint16(g)
			dst[j+2] = uint16(bl)
			dst[j+3] = uint16(a)
			j += 4
		}
	}
}
