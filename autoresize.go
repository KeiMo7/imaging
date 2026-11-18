package imaging

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"
	"time"
)

// SceneHint is an optional caller-provided description of the image content.
// A non-empty hint takes precedence over metadata-based classification.
type SceneHint string

const (
	// SceneHintAuto asks the policy to classify the image from metadata.
	SceneHintAuto SceneHint = ""
	// SceneHintPortrait declares a photographic portrait.
	SceneHintPortrait SceneHint = "portrait"
	// SceneHintScreenshot declares a screenshot or other text-heavy graphic.
	SceneHintScreenshot SceneHint = "screenshot"
	// SceneHintIcon declares an icon or other line-art graphic.
	SceneHintIcon SceneHint = "icon"
)

// Short aliases for callers that prefer the Scene prefix.
const (
	SceneAuto       = SceneHintAuto
	ScenePortrait   = SceneHintPortrait
	SceneScreenshot = SceneHintScreenshot
	SceneIcon       = SceneHintIcon
)

// ImageKind describes the broad visual category used by the resize policy.
type ImageKind string

const (
	// ImageKindUnknown means the caller did not provide a reliable category.
	ImageKindUnknown ImageKind = ""
	// ImageKindPhoto is a photograph or other continuous-tone image.
	ImageKindPhoto ImageKind = "photo"
	// ImageKindGraphic is line art, a screenshot, an icon, or text-heavy art.
	ImageKindGraphic ImageKind = "graphic"
)

// InputMetadata contains optional information about the input that does not
// require scanning its pixels. Fields ending in Set distinguish an explicit
// false value from an unspecified value.
type InputMetadata struct {
	Kind         ImageKind `json:"kind,omitempty"`
	SourceFormat string    `json:"source_format,omitempty"`
	Channels     int       `json:"channels,omitempty"`
	Grayscale    bool      `json:"grayscale"`
	GrayscaleSet bool      `json:"grayscale_set,omitempty"`
	HasAlpha     bool      `json:"has_alpha"`
	HasAlphaSet  bool      `json:"has_alpha_set,omitempty"`
}

// ResizeInputMetadata is kept as a descriptive alias for InputMetadata.
type ResizeInputMetadata = InputMetadata

// ResizeStep records one prior resize in a scaling chain.
type ResizeStep struct {
	SrcWidth   int    `json:"src_width"`
	SrcHeight  int    `json:"src_height"`
	DstWidth   int    `json:"dst_width"`
	DstHeight  int    `json:"dst_height"`
	FilterName string `json:"filter"`
}

// ResizeHistory describes resampling that happened before the current request.
// CumulativeDownscale, when positive, overrides Steps and is expressed as a
// linear factor (for example 2 means the current image is half the size).
type ResizeHistory struct {
	Steps               []ResizeStep `json:"steps,omitempty"`
	CumulativeDownscale float64      `json:"cumulative_downscale,omitempty"`
}

// AppendResizeStep returns a history with one prior resize appended.
func AppendResizeStep(history ResizeHistory, srcWidth, srcHeight, dstWidth, dstHeight int, filterName string) ResizeHistory {
	out := ResizeHistory{
		Steps:               append([]ResizeStep(nil), history.Steps...),
		CumulativeDownscale: history.CumulativeDownscale,
	}
	out.Steps = append(out.Steps, ResizeStep{
		SrcWidth:   srcWidth,
		SrcHeight:  srcHeight,
		DstWidth:   dstWidth,
		DstHeight:  dstHeight,
		FilterName: filterName,
	})
	return out
}

// ScaleMode describes the direction of a resize operation.
type ScaleMode string

const (
	ScaleModeNone      ScaleMode = "none"
	ScaleModeDownscale ScaleMode = "downscale"
	ScaleModeUpscale   ScaleMode = "upscale"
	ScaleModeMixed     ScaleMode = "mixed"
)

// DownscaleTier describes how aggressive a downscale is.
type DownscaleTier string

const (
	DownscaleTierNone   DownscaleTier = ""
	DownscaleTierHeavy  DownscaleTier = "heavy"
	DownscaleTierMedium DownscaleTier = "medium"
	DownscaleTierLight  DownscaleTier = "light"
)

// AutoResizePolicy configures filter selection. The zero value is disabled;
// use DefaultAutoResizePolicy to opt in.
type AutoResizePolicy struct {
	Enabled                  bool
	HeavyDownscaleThreshold  float64
	LightDownscaleThreshold  float64
	MaxCumulativeDownscale   float64
	AllowCumulativeDownscale bool
}

// DefaultAutoResizePolicy returns the opt-in policy used by the automatic
// resize entry points. Existing Resize/Fit/Fill behavior is unaffected.
func DefaultAutoResizePolicy() AutoResizePolicy {
	return AutoResizePolicy{
		Enabled:                 true,
		HeavyDownscaleThreshold: 0.35,
		LightDownscaleThreshold: 0.75,
		MaxCumulativeDownscale:  4.0,
	}
}

var (
	// ErrAutoResizeDisabled means the caller did not explicitly opt in.
	ErrAutoResizeDisabled = errors.New("imaging: automatic resize policy is disabled")
	// ErrInvalidImageSize means the source image has a non-positive dimension.
	ErrInvalidImageSize = errors.New("imaging: invalid image size")
	// ErrInvalidResizeSize means a target dimension is non-positive.
	ErrInvalidResizeSize = errors.New("imaging: invalid resize size")
	// ErrInvalidAutoResizePolicy means a policy threshold is invalid.
	ErrInvalidAutoResizePolicy = errors.New("imaging: invalid automatic resize policy")
	// ErrCumulativeRescale means the policy refused a blur-accumulating chain.
	ErrCumulativeRescale = errors.New("imaging: cumulative downscale limit exceeded")
)

// ResizeDecisionInput is the complete, image-independent input to filter
// selection. It contains dimensions, channel metadata, and optional hints only.
type ResizeDecisionInput struct {
	SrcWidth  int
	SrcHeight int
	DstWidth  int
	DstHeight int
	Hint      SceneHint
	Metadata  InputMetadata
	History   ResizeHistory
}

// ResizeDecision explains why a filter was selected. Filter is used by the
// resampling layer; FilterName and the evidence fields are JSON-friendly.
type ResizeDecision struct {
	FilterName string         `json:"filter"`
	Filter     ResampleFilter `json:"-"`

	Mode           ScaleMode     `json:"mode"`
	Tier           DownscaleTier `json:"tier,omitempty"`
	ImageKind      ImageKind     `json:"image_kind"`
	KindSource     string        `json:"kind_source"`
	SceneHint      SceneHint     `json:"scene_hint,omitempty"`
	ScaleRatioX    float64       `json:"scale_ratio_x"`
	ScaleRatioY    float64       `json:"scale_ratio_y"`
	EffectiveScale float64       `json:"effective_scale"`
	Uniform        bool          `json:"uniform"`
	SourceAspect   float64       `json:"source_aspect"`
	NearlySquare   bool          `json:"nearly_square"`

	Channels  int  `json:"channels"`
	Grayscale bool `json:"grayscale"`
	HasAlpha  bool `json:"has_alpha"`

	CumulativeDownscale float64 `json:"cumulative_downscale"`
	CumulativeLimit     float64 `json:"cumulative_limit"`
	CumulativeExceeded  bool    `json:"cumulative_exceeded"`

	Warning string   `json:"warning,omitempty"`
	Reasons []string `json:"reasons"`
}

type normalizedAutoResizePolicy struct {
	heavyThreshold  float64
	lightThreshold  float64
	maxCumulative   float64
	allowCumulative bool
}

func normalizeAutoResizePolicy(policy AutoResizePolicy) (normalizedAutoResizePolicy, error) {
	if !policy.Enabled {
		return normalizedAutoResizePolicy{}, ErrAutoResizeDisabled
	}

	out := normalizedAutoResizePolicy{
		heavyThreshold:  policy.HeavyDownscaleThreshold,
		lightThreshold:  policy.LightDownscaleThreshold,
		maxCumulative:   policy.MaxCumulativeDownscale,
		allowCumulative: policy.AllowCumulativeDownscale,
	}
	if out.heavyThreshold < 0 || out.lightThreshold < 0 || out.maxCumulative < 0 {
		return normalizedAutoResizePolicy{}, fmt.Errorf("%w: thresholds must not be negative", ErrInvalidAutoResizePolicy)
	}
	if out.heavyThreshold == 0 {
		out.heavyThreshold = 0.35
	}
	if out.lightThreshold == 0 {
		out.lightThreshold = 0.75
	}
	if out.maxCumulative == 0 {
		out.maxCumulative = 4
	}
	if out.heavyThreshold >= 1 || out.lightThreshold >= 1 ||
		out.heavyThreshold > out.lightThreshold || out.maxCumulative < 1 {
		return normalizedAutoResizePolicy{}, fmt.Errorf(
			"%w: require 0 < heavy <= light < 1 and max cumulative >= 1",
			ErrInvalidAutoResizePolicy,
		)
	}
	return out, nil
}

// DecideResize selects a resampling filter without touching image pixels.
// It is intentionally independent of Resize so that policy behavior can be
// unit tested with dimensions and metadata alone.
func DecideResize(in ResizeDecisionInput, policy AutoResizePolicy) (ResizeDecision, error) {
	norm, err := normalizeAutoResizePolicy(policy)
	if err != nil {
		return ResizeDecision{}, err
	}
	if in.SrcWidth <= 0 || in.SrcHeight <= 0 {
		return ResizeDecision{}, fmt.Errorf(
			"%w: source is %dx%d",
			ErrInvalidImageSize, in.SrcWidth, in.SrcHeight,
		)
	}
	if in.DstWidth <= 0 || in.DstHeight <= 0 {
		return ResizeDecision{}, fmt.Errorf(
			"%w: target is %dx%d",
			ErrInvalidResizeSize, in.DstWidth, in.DstHeight,
		)
	}

	ratioX := float64(in.DstWidth) / float64(in.SrcWidth)
	ratioY := float64(in.DstHeight) / float64(in.SrcHeight)
	mode, effectiveScale, tier := classifyScale(ratioX, ratioY, norm)
	uniform := math.Abs(ratioX-ratioY) <= 1e-9

	kind, kindSource, kindReason := classifyImage(in.Hint, in.Metadata.Kind)
	channels := in.Metadata.Channels
	if channels <= 0 {
		channels = 4
	}

	decision := ResizeDecision{
		Mode:            mode,
		Tier:            tier,
		ImageKind:       kind,
		KindSource:      kindSource,
		SceneHint:       in.Hint,
		ScaleRatioX:     ratioX,
		ScaleRatioY:     ratioY,
		EffectiveScale:  effectiveScale,
		Uniform:         uniform,
		SourceAspect:    float64(in.SrcWidth) / float64(in.SrcHeight),
		NearlySquare:    isNearlySquare(in.SrcWidth, in.SrcHeight),
		Channels:        channels,
		Grayscale:       in.Metadata.Grayscale,
		HasAlpha:        in.Metadata.HasAlpha,
		CumulativeLimit: norm.maxCumulative,
	}
	decision.FilterName, decision.Filter = selectAutoFilter(decision)
	decision.Reasons = append(decision.Reasons,
		kindReason,
		fmt.Sprintf("scale=%.4f mode=%s tier=%s", effectiveScale, mode, tier),
		aspectReason(ratioX, ratioY, uniform),
		fmt.Sprintf("source_aspect=%.4f nearly_square=%t", decision.SourceAspect, decision.NearlySquare),
		fmt.Sprintf("channels=%d grayscale=%t alpha=%t", channels, decision.Grayscale, decision.HasAlpha),
		filterRationale(decision),
	)

	decision.CumulativeDownscale = projectedCumulativeDownscale(in.History)
	if decision.CumulativeDownscale > 1 && mode == ScaleModeDownscale && effectiveScale > 0 {
		decision.CumulativeDownscale *= 1 / effectiveScale
	}
	decision.CumulativeExceeded = decision.CumulativeDownscale > 1 &&
		decision.CumulativeDownscale > norm.maxCumulative
	if decision.CumulativeExceeded {
		decision.Warning = fmt.Sprintf(
			"cumulative downscale %.2fx exceeds %.2fx; resize directly from the original image instead of resampling this image again",
			decision.CumulativeDownscale, norm.maxCumulative,
		)
		decision.Reasons = append(decision.Reasons, "cumulative_limit_exceeded")
		if !norm.allowCumulative {
			return decision, fmt.Errorf("%w: %.2fx > %.2fx", ErrCumulativeRescale, decision.CumulativeDownscale, norm.maxCumulative)
		}
	} else {
		decision.Reasons = append(decision.Reasons,
			fmt.Sprintf("cumulative_downscale=%.2f limit=%.2f", decision.CumulativeDownscale, norm.maxCumulative),
		)
	}

	return decision, nil
}

func classifyScale(ratioX, ratioY float64, policy normalizedAutoResizePolicy) (ScaleMode, float64, DownscaleTier) {
	if ratioX == 1 && ratioY == 1 {
		return ScaleModeNone, 1, DownscaleTierNone
	}

	if ratioX <= 1 && ratioY <= 1 {
		scale := math.Min(ratioX, ratioY)
		tier := DownscaleTierLight
		if scale <= policy.heavyThreshold {
			tier = DownscaleTierHeavy
		} else if scale <= policy.lightThreshold {
			tier = DownscaleTierMedium
		}
		return ScaleModeDownscale, scale, tier
	}

	if ratioX >= 1 && ratioY >= 1 {
		return ScaleModeUpscale, math.Max(ratioX, ratioY), DownscaleTierNone
	}

	return ScaleModeMixed, math.Max(ratioX, ratioY), DownscaleTierNone
}

func classifyImage(hint SceneHint, metadataKind ImageKind) (ImageKind, string, string) {
	switch hint {
	case SceneHintPortrait:
		return ImageKindPhoto, "caller_hint", "caller_hint=portrait overrides automatic classification"
	case SceneHintScreenshot, SceneHintIcon:
		return ImageKindGraphic, "caller_hint", fmt.Sprintf("caller_hint=%s overrides automatic classification", hint)
	}

	if metadataKind == ImageKindPhoto || metadataKind == ImageKindGraphic {
		return metadataKind, "input_metadata", fmt.Sprintf("image_kind=%s from input metadata", metadataKind)
	}

	reason := "image_kind=unknown; using conservative filter"
	if hint != SceneHintAuto {
		reason = fmt.Sprintf("scene_hint=%s is not recognized; image_kind=unknown; using conservative filter", hint)
	}
	return ImageKindUnknown, "conservative_fallback", reason
}

func selectAutoFilter(decision ResizeDecision) (string, ResampleFilter) {
	if decision.Mode == ScaleModeNone {
		return "NearestNeighbor", NearestNeighbor
	}
	if decision.Mode == ScaleModeMixed {
		return "Linear", Linear
	}

	if decision.Mode == ScaleModeUpscale {
		switch decision.ImageKind {
		case ImageKindPhoto:
			return "MitchellNetravali", MitchellNetravali
		case ImageKindGraphic:
			return "NearestNeighbor", NearestNeighbor
		default:
			return "Linear", Linear
		}
	}

	switch decision.ImageKind {
	case ImageKindPhoto:
		switch decision.Tier {
		case DownscaleTierHeavy:
			return "Lanczos", Lanczos
		case DownscaleTierMedium:
			return "CatmullRom", CatmullRom
		default:
			return "Linear", Linear
		}
	case ImageKindGraphic:
		switch decision.Tier {
		case DownscaleTierHeavy, DownscaleTierMedium:
			return "Box", Box
		default:
			return "NearestNeighbor", NearestNeighbor
		}
	default:
		return "Linear", Linear
	}
}

func filterRationale(decision ResizeDecision) string {
	switch {
	case decision.Mode == ScaleModeNone:
		return "filter=NearestNeighbor: dimensions are unchanged"
	case decision.Mode == ScaleModeMixed:
		return "filter=Linear: mixed-axis scaling uses the conservative non-ringing filter"
	case decision.Mode == ScaleModeUpscale && decision.ImageKind == ImageKindPhoto:
		return "filter=MitchellNetravali: photographic upscale favors smooth interpolation"
	case decision.Mode == ScaleModeUpscale && decision.ImageKind == ImageKindGraphic:
		return "filter=NearestNeighbor: graphic upscale preserves hard edges"
	case decision.Mode == ScaleModeUpscale:
		return "filter=Linear: unknown image kind uses conservative smooth interpolation"
	case decision.ImageKind == ImageKindPhoto && decision.Tier == DownscaleTierHeavy:
		return "filter=Lanczos: aggressive photo downscale retains detail with anti-aliasing"
	case decision.ImageKind == ImageKindPhoto && decision.Tier == DownscaleTierMedium:
		return "filter=CatmullRom: moderate photo downscale balances sharpness and ringing"
	case decision.ImageKind == ImageKindPhoto:
		return "filter=Linear: close-to-1 photo downscale avoids over-smoothing"
	case decision.ImageKind == ImageKindGraphic && decision.Tier != DownscaleTierLight:
		return "filter=Box: text and line-art downscale averages pixels without cubic over-smoothing"
	case decision.ImageKind == ImageKindGraphic:
		return "filter=NearestNeighbor: slight graphic downscale preserves hard pixel edges"
	default:
		return "filter=Linear: unknown image kind falls back to the conservative non-ringing filter"
	}
}

func isNearlySquare(width, height int) bool {
	if width <= 0 || height <= 0 {
		return false
	}
	aspect := float64(width) / float64(height)
	return aspect >= 0.95 && aspect <= 1.05
}

func aspectReason(ratioX, ratioY float64, uniform bool) string {
	if uniform {
		return "aspect=uniform"
	}
	return fmt.Sprintf("aspect=non_uniform ratio_x=%.4f ratio_y=%.4f", ratioX, ratioY)
}

func projectedCumulativeDownscale(history ResizeHistory) float64 {
	if history.CumulativeDownscale > 0 {
		return history.CumulativeDownscale
	}

	factor := 1.0
	for _, step := range history.Steps {
		if step.SrcWidth <= 0 || step.SrcHeight <= 0 || step.DstWidth <= 0 || step.DstHeight <= 0 {
			continue
		}
		stepFactor := 1.0
		if step.DstWidth < step.SrcWidth {
			stepFactor = math.Max(stepFactor, float64(step.SrcWidth)/float64(step.DstWidth))
		}
		if step.DstHeight < step.SrcHeight {
			stepFactor = math.Max(stepFactor, float64(step.SrcHeight)/float64(step.DstHeight))
		}
		factor *= stepFactor
	}
	return factor
}

// AutoResizeOptions controls the opt-in automatic resize entry point.
type AutoResizeOptions struct {
	Policy   AutoResizePolicy
	Hint     SceneHint
	Metadata InputMetadata
	History  ResizeHistory
}

// DefaultAutoResizeOptions returns explicitly enabled default options.
func DefaultAutoResizeOptions() AutoResizeOptions {
	return AutoResizeOptions{Policy: DefaultAutoResizePolicy()}
}

// ResizeReportInput records the request used for one automatic resize run.
type ResizeReportInput struct {
	Width        int           `json:"width"`
	Height       int           `json:"height"`
	TargetWidth  int           `json:"target_width"`
	TargetHeight int           `json:"target_height"`
	Hint         SceneHint     `json:"scene_hint,omitempty"`
	Metadata     InputMetadata `json:"metadata"`
	History      ResizeHistory `json:"history,omitempty"`
}

// ResizeReportOutput records the actual output and measured runtime.
type ResizeReportOutput struct {
	Width      int           `json:"width"`
	Height     int           `json:"height"`
	Duration   time.Duration `json:"duration_ns"`
	DurationMS float64       `json:"duration_ms"`
}

// ResizeReport combines the policy decision with the actual result of the same
// run. On error, Output is nil and no partial image is returned.
type ResizeReport struct {
	Status    string              `json:"status"`
	Operation string              `json:"operation"`
	Input     ResizeReportInput   `json:"input"`
	Decision  ResizeDecision      `json:"decision"`
	Output    *ResizeReportOutput `json:"output,omitempty"`
	Error     string              `json:"error,omitempty"`
	Warning   string              `json:"warning,omitempty"`
}

// ResizeAuto selects a filter using ResizeDecisionInput and then calls the
// existing Resize implementation. A policy must be explicitly enabled.
func ResizeAuto(img image.Image, width, height int, options AutoResizeOptions) (out *image.NRGBA, report *ResizeReport, err error) {
	report = &ResizeReport{
		Status:    "error",
		Operation: "auto_resize",
		Input: ResizeReportInput{
			TargetWidth:  width,
			TargetHeight: height,
			Hint:         options.Hint,
			Metadata:     options.Metadata,
			History:      options.History,
		},
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			out = nil
			report.Status = "error"
			report.Error = fmt.Sprintf("automatic resize failed: %v", recovered)
			err = fmt.Errorf("imaging: automatic resize failed: %v", recovered)
		}
	}()

	if img == nil {
		err = fmt.Errorf("%w: source image is nil", ErrInvalidImageSize)
		report.Error = err.Error()
		return nil, report, err
	}

	bounds := img.Bounds()
	srcWidth, srcHeight := bounds.Dx(), bounds.Dy()
	report.Input.Width = srcWidth
	report.Input.Height = srcHeight
	if srcWidth <= 0 || srcHeight <= 0 {
		err = fmt.Errorf("%w: source is %dx%d", ErrInvalidImageSize, srcWidth, srcHeight)
		report.Error = err.Error()
		return nil, report, err
	}
	if width <= 0 || height <= 0 {
		err = fmt.Errorf("%w: target is %dx%d", ErrInvalidResizeSize, width, height)
		report.Error = err.Error()
		return nil, report, err
	}

	metadata := mergeInputMetadata(inspectImageMetadata(img), options.Metadata)
	report.Input.Metadata = metadata
	decision, decisionErr := DecideResize(ResizeDecisionInput{
		SrcWidth:  srcWidth,
		SrcHeight: srcHeight,
		DstWidth:  width,
		DstHeight: height,
		Hint:      options.Hint,
		Metadata:  metadata,
		History:   options.History,
	}, options.Policy)
	report.Decision = decision
	report.Warning = decision.Warning
	if decisionErr != nil {
		err = decisionErr
		report.Error = err.Error()
		return nil, report, err
	}

	start := time.Now()
	out = Resize(img, width, height, decision.Filter)
	elapsed := time.Since(start)
	if out == nil || out.Bounds().Dx() <= 0 || out.Bounds().Dy() <= 0 {
		out = nil
		err = errors.New("imaging: resize produced an empty image")
		report.Error = err.Error()
		return nil, report, err
	}

	report.Status = "success"
	report.Output = &ResizeReportOutput{
		Width:      out.Bounds().Dx(),
		Height:     out.Bounds().Dy(),
		Duration:   elapsed,
		DurationMS: float64(elapsed) / float64(time.Millisecond),
	}
	return out, report, nil
}

// AutoResize is an alias for ResizeAuto.
func AutoResize(img image.Image, width, height int, options AutoResizeOptions) (*image.NRGBA, *ResizeReport, error) {
	return ResizeAuto(img, width, height, options)
}

func inspectImageMetadata(img image.Image) InputMetadata {
	meta := InputMetadata{}
	switch typed := img.(type) {
	case *image.Gray:
		meta.Channels, meta.Grayscale, meta.HasAlpha = 1, true, false
		return meta
	case *image.Gray16:
		meta.Channels, meta.Grayscale, meta.HasAlpha = 1, true, false
		return meta
	case *image.Alpha:
		meta.Channels, meta.HasAlpha = 1, true
		return meta
	case *image.Alpha16:
		meta.Channels, meta.HasAlpha = 1, true
		return meta
	case *image.YCbCr:
		meta.Channels = 3
		return meta
	case *image.CMYK:
		meta.Channels = 4
		return meta
	case *image.Paletted:
		meta.Channels = 4
		for _, entry := range typed.Palette {
			nrgba := color.NRGBAModel.Convert(entry).(color.NRGBA)
			if nrgba.A != 0xff {
				meta.HasAlpha = true
			}
			if !meta.GrayscaleSet {
				meta.Grayscale = nrgba.R == nrgba.G && nrgba.G == nrgba.B
				meta.GrayscaleSet = true
			} else {
				meta.Grayscale = meta.Grayscale && nrgba.R == nrgba.G && nrgba.G == nrgba.B
			}
		}
		if len(typed.Palette) == 0 {
			meta.GrayscaleSet = false
		}
		return meta
	}

	switch img.ColorModel() {
	case color.GrayModel, color.Gray16Model:
		meta.Channels, meta.Grayscale, meta.HasAlpha = 1, true, false
	case color.NRGBAModel, color.NRGBA64Model, color.RGBAModel, color.RGBA64Model:
		meta.Channels, meta.HasAlpha = 4, true
	case color.YCbCrModel:
		meta.Channels = 3
	case color.CMYKModel:
		meta.Channels = 4
	default:
		meta.Channels = 4
	}
	return meta
}

func mergeInputMetadata(inferred, supplied InputMetadata) InputMetadata {
	out := inferred
	if supplied.Kind != ImageKindUnknown {
		out.Kind = supplied.Kind
	}
	if supplied.SourceFormat != "" {
		out.SourceFormat = supplied.SourceFormat
	}
	if supplied.Channels > 0 {
		out.Channels = supplied.Channels
	}
	if supplied.GrayscaleSet {
		out.Grayscale = supplied.Grayscale
		out.GrayscaleSet = true
	}
	if supplied.HasAlphaSet {
		out.HasAlpha = supplied.HasAlpha
		out.HasAlphaSet = true
	}
	return out
}

// ResizeBatchJob is one item in a best-effort automatic resize batch. Image
// takes precedence over Path; Path is decoded per run so a repaired file can be
// retried without rebuilding the whole batch.
type ResizeBatchJob struct {
	ID       string
	Path     string
	Image    image.Image
	Width    int
	Height   int
	Hint     SceneHint
	Metadata InputMetadata
	History  ResizeHistory
}

// ResizeBatchItem is the result for one batch job.
type ResizeBatchItem struct {
	ID         string          `json:"id"`
	Path       string          `json:"path,omitempty"`
	Image      *image.NRGBA    `json:"-"`
	Report     *ResizeReport   `json:"report"`
	Skipped    bool            `json:"skipped"`
	SkipReason string          `json:"skip_reason,omitempty"`
	RetryJob   *ResizeBatchJob `json:"-"`
}

// ResizeBatchSummary summarizes a best-effort batch run.
type ResizeBatchSummary struct {
	Total     int `json:"total"`
	Succeeded int `json:"succeeded"`
	Skipped   int `json:"skipped"`
}

// ResizeBatchResult contains one report per submitted job. A bad item never
// prevents the remaining jobs from running.
type ResizeBatchResult struct {
	Items   []ResizeBatchItem  `json:"items"`
	Summary ResizeBatchSummary `json:"summary"`
}

// ResizeBatch runs each job independently. Decode, validation, and policy
// failures are recorded as skipped items with retryable job descriptions.
func ResizeBatch(jobs []ResizeBatchJob, policy AutoResizePolicy) ResizeBatchResult {
	result := ResizeBatchResult{
		Items:   make([]ResizeBatchItem, 0, len(jobs)),
		Summary: ResizeBatchSummary{Total: len(jobs)},
	}

	for _, job := range jobs {
		item := ResizeBatchItem{ID: job.ID, Path: job.Path}
		img := job.Image
		metadata := job.Metadata
		if img == nil && job.Path != "" {
			decoded, err := Open(job.Path)
			if err != nil {
				report := skippedReport(job, "decode_failed: "+err.Error())
				item.Report = report
				item.Skipped = true
				item.SkipReason = report.Error
				retry := job
				item.RetryJob = &retry
				result.Summary.Skipped++
				result.Items = append(result.Items, item)
				continue
			}
			img = decoded
			if metadata.SourceFormat == "" {
				if format, err := FormatFromFilename(job.Path); err == nil {
					metadata.SourceFormat = format.String()
				}
			}
		}
		if img == nil {
			report := skippedReport(job, "missing_source: job has neither Image nor Path")
			item.Report = report
			item.Skipped = true
			item.SkipReason = report.Error
			retry := job
			item.RetryJob = &retry
			result.Summary.Skipped++
			result.Items = append(result.Items, item)
			continue
		}

		out, report, err := ResizeAuto(img, job.Width, job.Height, AutoResizeOptions{
			Policy:   policy,
			Hint:     job.Hint,
			Metadata: metadata,
			History:  job.History,
		})
		item.Report = report
		if err != nil {
			item.Skipped = true
			item.SkipReason = err.Error()
			retry := job
			item.RetryJob = &retry
			result.Summary.Skipped++
			result.Items = append(result.Items, item)
			continue
		}

		item.Image = out
		result.Summary.Succeeded++
		result.Items = append(result.Items, item)
	}

	return result
}

func skippedReport(job ResizeBatchJob, reason string) *ResizeReport {
	return &ResizeReport{
		Status:    "skipped",
		Operation: "auto_resize",
		Input: ResizeReportInput{
			TargetWidth:  job.Width,
			TargetHeight: job.Height,
			Hint:         job.Hint,
			Metadata:     job.Metadata,
			History:      job.History,
		},
		Error:   reason,
		Warning: "",
	}
}

// SkippedJobs returns retryable copies of all skipped jobs in original order.
func (result ResizeBatchResult) SkippedJobs() []ResizeBatchJob {
	jobs := make([]ResizeBatchJob, 0, result.Summary.Skipped)
	for _, item := range result.Items {
		if item.Skipped && item.RetryJob != nil {
			jobs = append(jobs, *item.RetryJob)
		}
	}
	return jobs
}

// RetrySkipped resubmits skipped jobs, optionally replacing them by ID. This
// is useful after repairing a truncated file or supplying a valid target size.
func (result ResizeBatchResult) RetrySkipped(replacements map[string]ResizeBatchJob, policy AutoResizePolicy) ResizeBatchResult {
	jobs := result.SkippedJobs()
	for i := range jobs {
		if replacement, ok := replacements[jobs[i].ID]; ok {
			jobs[i] = replacement
		}
	}
	return ResizeBatch(jobs, policy)
}
