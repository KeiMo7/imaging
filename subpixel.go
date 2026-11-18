package imaging

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"
	"strings"
)

// Errors returned by CropSubpixel.
var (
	ErrCropOutside           = errors.New("imaging: crop rectangle is entirely outside the source image")
	ErrEmptySource           = errors.New("imaging: source image is empty")
	ErrInvalidCropRect       = errors.New("imaging: crop rectangle must be finite and non-empty")
	ErrInvalidFillMode       = errors.New("imaging: invalid fill mode")
	ErrInvalidOutputSize     = errors.New("imaging: output size must be positive")
	ErrInvalidRoundingMode   = errors.New("imaging: invalid rounding mode")
	ErrFillColorChannels     = errors.New("imaging: fill color channel count mismatch")
	ErrUnknownResampleFilter = errors.New("imaging: unknown resample filter")
)

// FloatRect is a rectangle with subpixel coordinates. Coordinates use the same
// image coordinate system as image.Rectangle.
type FloatRect struct {
	MinX float64 `json:"minX"`
	MinY float64 `json:"minY"`
	MaxX float64 `json:"maxX"`
	MaxY float64 `json:"maxY"`
}

// RectF is an alias for FloatRect.
type RectF = FloatRect

// FloatRectFrom converts an integer rectangle to a FloatRect.
func FloatRectFrom(rect image.Rectangle) FloatRect {
	return FloatRect{
		MinX: float64(rect.Min.X),
		MinY: float64(rect.Min.Y),
		MaxX: float64(rect.Max.X),
		MaxY: float64(rect.Max.Y),
	}
}

// Dx returns the rectangle width.
func (r FloatRect) Dx() float64 {
	return r.MaxX - r.MinX
}

// Dy returns the rectangle height.
func (r FloatRect) Dy() float64 {
	return r.MaxY - r.MinY
}

// Empty reports whether the rectangle has zero or negative area.
func (r FloatRect) Empty() bool {
	return r.Dx() <= 0 || r.Dy() <= 0
}

// Intersect returns the intersection of r and s.
func (r FloatRect) Intersect(s FloatRect) FloatRect {
	return FloatRect{
		MinX: math.Max(r.MinX, s.MinX),
		MinY: math.Max(r.MinY, s.MinY),
		MaxX: math.Min(r.MaxX, s.MaxX),
		MaxY: math.Min(r.MaxY, s.MaxY),
	}
}

// FillMode selects how samples outside one edge of the source image are filled.
type FillMode int

// Supported fill modes.
const (
	FillConstant FillMode = iota
	FillEdge
	FillMirror
)

func (m FillMode) String() string {
	switch m {
	case FillConstant:
		return "constant"
	case FillEdge:
		return "edge"
	case FillMirror:
		return "mirror"
	default:
		return fmt.Sprintf("fill-mode(%d)", int(m))
	}
}

// MarshalText implements encoding.TextMarshaler.
func (m FillMode) MarshalText() ([]byte, error) {
	if m < FillConstant || m > FillMirror {
		return nil, fmt.Errorf("%w: %d", ErrInvalidFillMode, int(m))
	}
	return []byte(m.String()), nil
}

// UnmarshalText implements encoding.TextUnmarshaler.
func (m *FillMode) UnmarshalText(text []byte) error {
	switch strings.ToLower(strings.TrimSpace(string(text))) {
	case "constant":
		*m = FillConstant
	case "edge", "clamp":
		*m = FillEdge
	case "mirror", "reflect":
		*m = FillMirror
	default:
		return fmt.Errorf("%w: %q", ErrInvalidFillMode, text)
	}
	return nil
}

// EdgeFillConfig selects a fill mode independently for each image edge.
type EdgeFillConfig struct {
	Top    FillMode `json:"top"`
	Right  FillMode `json:"right"`
	Bottom FillMode `json:"bottom"`
	Left   FillMode `json:"left"`
}

// Validate reports whether all edge fill modes are valid.
func (c EdgeFillConfig) Validate() error {
	modes := [...]struct {
		name string
		mode FillMode
	}{
		{"top", c.Top},
		{"right", c.Right},
		{"bottom", c.Bottom},
		{"left", c.Left},
	}
	for _, item := range modes {
		name, mode := item.name, item.mode
		if mode < FillConstant || mode > FillMirror {
			return fmt.Errorf("%w on %s edge: %d", ErrInvalidFillMode, name, int(mode))
		}
	}
	return nil
}

// RoundingMode selects how fractional output dimensions are converted to pixels.
type RoundingMode int

// Supported output size rounding modes.
const (
	RoundHalfUp RoundingMode = iota
	RoundDown
	RoundUp
	RoundTowardZero
	RoundAwayFromZero
)

func (m RoundingMode) String() string {
	switch m {
	case RoundHalfUp:
		return "half-up"
	case RoundDown:
		return "down"
	case RoundUp:
		return "up"
	case RoundTowardZero:
		return "toward-zero"
	case RoundAwayFromZero:
		return "away-from-zero"
	default:
		return fmt.Sprintf("rounding-mode(%d)", int(m))
	}
}

// MarshalText implements encoding.TextMarshaler.
func (m RoundingMode) MarshalText() ([]byte, error) {
	if m < RoundHalfUp || m > RoundAwayFromZero {
		return nil, fmt.Errorf("%w: %d", ErrInvalidRoundingMode, int(m))
	}
	return []byte(m.String()), nil
}

// UnmarshalText implements encoding.TextUnmarshaler.
func (m *RoundingMode) UnmarshalText(text []byte) error {
	switch strings.ToLower(strings.TrimSpace(string(text))) {
	case "half-up", "round", "nearest":
		*m = RoundHalfUp
	case "down", "floor":
		*m = RoundDown
	case "up", "ceil":
		*m = RoundUp
	case "toward-zero", "truncate":
		*m = RoundTowardZero
	case "away-from-zero":
		*m = RoundAwayFromZero
	default:
		return fmt.Errorf("%w: %q", ErrInvalidRoundingMode, text)
	}
	return nil
}

// SubpixelCropOptions configures CropSubpixel.
//
// Width and Height are the requested output dimensions before rounding. A zero
// value derives that dimension from the crop rectangle. FillColor contains one,
// three, or four non-premultiplied channel values matching the source image's
// semantic channel count: grayscale, RGB, or RGBA respectively. A nil FillColor
// uses a zero value appropriate for the source channel count.
//
// FilterName selects one of the package's resampling filters by name. When it
// is empty, Filter is used; if Filter is also zero, Linear is used.
type SubpixelCropOptions struct {
	Width      float64        `json:"width,omitempty"`
	Height     float64        `json:"height,omitempty"`
	Rounding   RoundingMode   `json:"rounding,omitempty"`
	FilterName string         `json:"filter,omitempty"`
	FillColor  []uint8        `json:"fillColor,omitempty"`
	Fill       EdgeFillConfig `json:"fill,omitempty"`
	Filter     ResampleFilter `json:"-"`
}

// CropMetadata describes the parameters and output shape of a subpixel crop.
// It is serializable and is sufficient to reconstruct the operation together
// with the source image and crop bounds.
type CropMetadata struct {
	SourceBounds    image.Rectangle `json:"sourceBounds"`
	CropBounds      FloatRect       `json:"cropBounds"`
	VisibleBounds   FloatRect       `json:"visibleBounds"`
	OutputBounds    image.Rectangle `json:"outputBounds"`
	RequestedWidth  float64         `json:"requestedWidth"`
	RequestedHeight float64         `json:"requestedHeight"`
	OutputWidth     int             `json:"outputWidth"`
	OutputHeight    int             `json:"outputHeight"`
	Rounding        RoundingMode    `json:"sizeRounding"`
	Filter          string          `json:"filter"`
	Fill            EdgeFillConfig  `json:"fill"`
	FillColor       []uint8         `json:"fillColor"`
	Channels        int             `json:"channels"`
}

// FillColorChannelError reports a fill color whose channel count does not
// match the source image.
type FillColorChannelError struct {
	Got      int
	Expected int
}

func (e *FillColorChannelError) Error() string {
	return fmt.Sprintf("imaging: fill color has %d channels, expected %d", e.Got, e.Expected)
}

// Unwrap allows errors.Is(err, ErrFillColorChannels).
func (e *FillColorChannelError) Unwrap() error {
	return ErrFillColorChannels
}

const fixedWeightScale int64 = 1 << 14

type fixedWeight struct {
	index  int
	weight int64
}

// CropSubpixel crops a fractional source rectangle and resamples it to the
// requested output dimensions.
//
// Coordinates outside the source are resolved independently for each edge
// according to options.Fill. The source rectangle must overlap the image, even
// when a constant fill is used. Output dimensions are rounded according to
// options.Rounding, which defaults to round-half-up.
//
// For a sample outside both a vertical and a horizontal edge, the vertical edge
// is resolved first. If it maps back into the source, the horizontal edge is
// then applied.
//
// The function either returns a complete image or an error. It never returns a
// partially written result.
func CropSubpixel(img image.Image, rect FloatRect, options ...SubpixelCropOptions) (*image.NRGBA, CropMetadata, error) {
	var opts SubpixelCropOptions
	if len(options) > 0 {
		opts = options[0]
	}

	if img == nil {
		return nil, CropMetadata{}, ErrEmptySource
	}
	sourceBounds := img.Bounds()
	sourceW := sourceBounds.Dx()
	sourceH := sourceBounds.Dy()
	if sourceW <= 0 || sourceH <= 0 {
		return nil, CropMetadata{}, ErrEmptySource
	}

	if !finite(rect.MinX) || !finite(rect.MinY) || !finite(rect.MaxX) || !finite(rect.MaxY) || rect.Empty() {
		return nil, CropMetadata{}, ErrInvalidCropRect
	}
	if !finite(opts.Width) || !finite(opts.Height) || opts.Width < 0 || opts.Height < 0 {
		return nil, CropMetadata{}, ErrInvalidOutputSize
	}
	if opts.Rounding < RoundHalfUp || opts.Rounding > RoundAwayFromZero {
		return nil, CropMetadata{}, fmt.Errorf("%w: %d", ErrInvalidRoundingMode, int(opts.Rounding))
	}
	if err := opts.Fill.Validate(); err != nil {
		return nil, CropMetadata{}, err
	}

	localRect := FloatRect{
		MinX: rect.MinX - float64(sourceBounds.Min.X),
		MinY: rect.MinY - float64(sourceBounds.Min.Y),
		MaxX: rect.MaxX - float64(sourceBounds.Min.X),
		MaxY: rect.MaxY - float64(sourceBounds.Min.Y),
	}
	sourceRect := FloatRect{MinX: 0, MinY: 0, MaxX: float64(sourceW), MaxY: float64(sourceH)}
	visible := localRect.Intersect(sourceRect)
	if visible.Empty() {
		return nil, CropMetadata{}, ErrCropOutside
	}

	requestedW := opts.Width
	if requestedW == 0 {
		requestedW = rect.Dx()
	}
	requestedH := opts.Height
	if requestedH == 0 {
		requestedH = rect.Dy()
	}
	outW, err := roundOutputDimension(requestedW, opts.Rounding)
	if err != nil {
		return nil, CropMetadata{}, err
	}
	outH, err := roundOutputDimension(requestedH, opts.Rounding)
	if err != nil {
		return nil, CropMetadata{}, err
	}
	if !validOutputPixels(outW, outH) {
		return nil, CropMetadata{}, ErrInvalidOutputSize
	}

	channels := sourceChannelCount(img)
	fillValues, fillColor, err := resolveFillColor(opts.FillColor, channels)
	if err != nil {
		return nil, CropMetadata{}, err
	}
	filter, filterName, err := resolveCropFilter(opts)
	if err != nil {
		return nil, CropMetadata{}, err
	}

	xWeights := precomputeCropWeights(outW, localRect.MinX, localRect.Dx(), filter)
	yWeights := precomputeCropWeights(outH, localRect.MinY, localRect.Dy(), filter)

	src := toNRGBA(img)
	dst := image.NewNRGBA(image.Rect(0, 0, outW, outH))
	parallel(0, outH, func(ys <-chan int) {
		for y := range ys {
			row := dst.Pix[y*dst.Stride : y*dst.Stride+outW*4]
			for x := 0; x < outW; x++ {
				sampleCropPixel(
					row[x*4:x*4+4],
					xWeights[x],
					yWeights[y],
					src,
					sourceW,
					sourceH,
					fillColor,
					opts.Fill,
				)
			}
		}
	})

	visibleAbs := FloatRect{
		MinX: visible.MinX + float64(sourceBounds.Min.X),
		MinY: visible.MinY + float64(sourceBounds.Min.Y),
		MaxX: visible.MaxX + float64(sourceBounds.Min.X),
		MaxY: visible.MaxY + float64(sourceBounds.Min.Y),
	}
	metadata := CropMetadata{
		SourceBounds:    sourceBounds,
		CropBounds:      rect,
		VisibleBounds:   visibleAbs,
		OutputBounds:    dst.Bounds(),
		RequestedWidth:  requestedW,
		RequestedHeight: requestedH,
		OutputWidth:     outW,
		OutputHeight:    outH,
		Rounding:        opts.Rounding,
		Filter:          filterName,
		Fill:            opts.Fill,
		FillColor:       append([]uint8(nil), fillValues...),
		Channels:        channels,
	}
	return dst, metadata, nil
}

// SubpixelCrop is an alias for CropSubpixel.
func SubpixelCrop(img image.Image, rect FloatRect, options ...SubpixelCropOptions) (*image.NRGBA, CropMetadata, error) {
	return CropSubpixel(img, rect, options...)
}

func finite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}

func roundOutputDimension(v float64, mode RoundingMode) (int, error) {
	maxInt := int(^uint(0) >> 1)
	if !finite(v) || v <= 0 || v >= float64(maxInt) {
		return 0, ErrInvalidOutputSize
	}

	var rounded float64
	switch mode {
	case RoundHalfUp:
		rounded = math.Floor(v + 0.5)
	case RoundDown:
		rounded = math.Floor(v)
	case RoundUp:
		rounded = math.Ceil(v)
	case RoundTowardZero:
		rounded = math.Trunc(v)
	case RoundAwayFromZero:
		if v >= 0 {
			rounded = math.Floor(v + 0.5)
		} else {
			rounded = math.Ceil(v - 0.5)
		}
	default:
		return 0, fmt.Errorf("%w: %d", ErrInvalidRoundingMode, int(mode))
	}
	if rounded <= 0 || rounded >= float64(maxInt) {
		return 0, ErrInvalidOutputSize
	}
	return int(rounded), nil
}

func validOutputPixels(w, h int) bool {
	if w <= 0 || h <= 0 {
		return false
	}
	maxInt := int(^uint(0) >> 1)
	if w > maxInt/4 {
		return false
	}
	return h <= maxInt/(w*4)
}

func sourceChannelCount(img image.Image) int {
	switch img.(type) {
	case *image.Gray, *image.Gray16, *image.Alpha, *image.Alpha16:
		return 1
	case *image.YCbCr:
		return 3
	case *image.NRGBA, *image.NRGBA64, *image.RGBA, *image.RGBA64, *image.Paletted, *image.NYCbCrA:
		return 4
	}

	switch img.ColorModel() {
	case color.GrayModel, color.Gray16Model, color.AlphaModel, color.Alpha16Model:
		return 1
	case color.YCbCrModel:
		return 3
	case color.RGBAModel, color.NRGBAModel, color.RGBA64Model, color.NRGBA64Model:
		return 4
	default:
		return 4
	}
}

func resolveFillColor(values []uint8, channels int) ([]uint8, color.NRGBA, error) {
	if channels != 1 && channels != 3 && channels != 4 {
		return nil, color.NRGBA{}, fmt.Errorf("%w: unsupported source channel count %d", ErrFillColorChannels, channels)
	}
	if len(values) == 0 {
		switch channels {
		case 1:
			values = []uint8{0}
		case 3:
			values = []uint8{0, 0, 0}
		case 4:
			values = []uint8{0, 0, 0, 0}
		}
	}
	if len(values) != channels {
		return nil, color.NRGBA{}, &FillColorChannelError{Got: len(values), Expected: channels}
	}

	copied := append([]uint8(nil), values...)
	switch channels {
	case 1:
		return copied, color.NRGBA{R: copied[0], G: copied[0], B: copied[0], A: 0xff}, nil
	case 3:
		return copied, color.NRGBA{R: copied[0], G: copied[1], B: copied[2], A: 0xff}, nil
	default:
		return copied, color.NRGBA{R: copied[0], G: copied[1], B: copied[2], A: copied[3]}, nil
	}
}

// ResampleFilterByName returns one of the package's built-in filters by name.
func ResampleFilterByName(name string) (ResampleFilter, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "nearest", "nearest-neighbor", "nearestneighbor":
		return NearestNeighbor, nil
	case "box":
		return Box, nil
	case "linear", "bilinear":
		return Linear, nil
	case "hermite":
		return Hermite, nil
	case "mitchellnetravali", "mitchell-netravali", "mitchell":
		return MitchellNetravali, nil
	case "catmullrom", "catmull-rom", "catrom":
		return CatmullRom, nil
	case "bspline", "b-spline":
		return BSpline, nil
	case "gaussian":
		return Gaussian, nil
	case "bartlett":
		return Bartlett, nil
	case "lanczos":
		return Lanczos, nil
	case "hann":
		return Hann, nil
	case "hamming":
		return Hamming, nil
	case "blackman":
		return Blackman, nil
	case "welch":
		return Welch, nil
	case "cosine":
		return Cosine, nil
	default:
		return ResampleFilter{}, fmt.Errorf("%w: %q", ErrUnknownResampleFilter, name)
	}
}

func resolveCropFilter(opts SubpixelCropOptions) (ResampleFilter, string, error) {
	if opts.FilterName != "" {
		filter, err := ResampleFilterByName(opts.FilterName)
		if err != nil {
			return ResampleFilter{}, "", err
		}
		return filter, strings.ToLower(strings.TrimSpace(opts.FilterName)), nil
	}
	if opts.Filter.Kernel != nil || opts.Filter.Support != 0 {
		return opts.Filter, "custom", nil
	}
	return Linear, "linear", nil
}

func precomputeCropWeights(dstSize int, start, span float64, filter ResampleFilter) [][]fixedWeight {
	if filter.Support <= 0 {
		weights := make([][]fixedWeight, dstSize)
		for v := 0; v < dstSize; v++ {
			center := start + (float64(v)+0.5)*span/float64(dstSize) - 0.5
			weights[v] = []fixedWeight{{index: int(math.Round(center)), weight: fixedWeightScale}}
		}
		return weights
	}

	raw := precomputeWeightsRange(dstSize, start, span, 0, 0, filter, false)
	return quantizeWeights(raw)
}

func quantizeWeights(raw [][]indexWeight) [][]fixedWeight {
	out := make([][]fixedWeight, len(raw))
	for i, weights := range raw {
		fixed := make([]fixedWeight, len(weights))
		var sum int64
		maxAbsIndex := 0
		var maxAbs int64
		for j, weight := range weights {
			q := int64(math.Round(weight.weight * float64(fixedWeightScale)))
			fixed[j] = fixedWeight{index: weight.index, weight: q}
			sum += q
			a := q
			if a < 0 {
				a = -a
			}
			if a > maxAbs {
				maxAbs = a
				maxAbsIndex = j
			}
		}
		if len(fixed) > 0 {
			fixed[maxAbsIndex].weight += fixedWeightScale - sum
		}
		out[i] = fixed
	}
	return out
}

func sampleCropPixel(
	dst []uint8,
	xWeights, yWeights []fixedWeight,
	src *image.NRGBA,
	sourceW, sourceH int,
	fill color.NRGBA,
	fillCfg EdgeFillConfig,
) {
	var r, g, b, a int64

	for _, yw := range yWeights {
		sy, yConstant := resolveSourceCoordinate(yw.index, sourceH, fillCfg.Top, fillCfg.Bottom)
		if yConstant {
			for _, xw := range xWeights {
				addWeightedPixel(&r, &g, &b, &a, fill, yw.weight*xw.weight)
			}
			continue
		}

		for _, xw := range xWeights {
			sx, xConstant := resolveSourceCoordinate(xw.index, sourceW, fillCfg.Left, fillCfg.Right)
			if xConstant {
				addWeightedPixel(&r, &g, &b, &a, fill, yw.weight*xw.weight)
				continue
			}

			offset := sy*src.Stride + sx*4
			pixel := color.NRGBA{
				R: src.Pix[offset],
				G: src.Pix[offset+1],
				B: src.Pix[offset+2],
				A: src.Pix[offset+3],
			}
			addWeightedPixel(&r, &g, &b, &a, pixel, yw.weight*xw.weight)
		}
	}

	if a != 0 {
		dst[0] = clamp(float64(r) / float64(a))
		dst[1] = clamp(float64(g) / float64(a))
		dst[2] = clamp(float64(b) / float64(a))
		dst[3] = clamp(float64(a) / float64(fixedWeightScale*fixedWeightScale))
	}
}

func addWeightedPixel(r, g, b, a *int64, pixel color.NRGBA, weight int64) {
	alphaWeight := int64(pixel.A) * weight
	*r += int64(pixel.R) * alphaWeight
	*g += int64(pixel.G) * alphaWeight
	*b += int64(pixel.B) * alphaWeight
	*a += alphaWeight
}

func resolveSourceCoordinate(v, size int, before, after FillMode) (int, bool) {
	var mode FillMode
	if v < 0 {
		mode = before
	} else if v >= size {
		mode = after
	} else {
		return v, false
	}

	switch mode {
	case FillConstant:
		return 0, true
	case FillEdge:
		if v < 0 {
			return 0, false
		}
		return size - 1, false
	case FillMirror:
		return mirrorIndex(v, size), false
	default:
		return 0, true
	}
}

func mirrorIndex(v, size int) int {
	if size <= 1 {
		return 0
	}
	period := 2 * size
	v %= period
	if v < 0 {
		v += period
	}
	if v >= size {
		v = period - 1 - v
	}
	return v
}
