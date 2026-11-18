package imaging

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"sync"
	"time"
)

// SearchMode controls how a fitting encoding is selected.
type SearchMode int

const (
	// QualityFirst selects the highest quality encoding that fits the target.
	QualityFirst SearchMode = iota

	// SizeFirst selects the smallest encoding that fits the target. For JPEG this
	// normally means the lowest quality that still fits.
	SizeFirst
)

// TargetSize search errors. Use errors.Is to distinguish the error categories.
var (
	ErrTargetSizeUnreachable    = errors.New("imaging: target size is unreachable")
	ErrTargetSizeIterationLimit = errors.New("imaging: target size search iteration limit reached")
)

// TargetSizeError describes a failed target-size search. The result returned
// together with this error still contains the closest encoding and its trace.
type TargetSizeError struct {
	Kind           error
	TargetBytes    int64
	MinimumBytes   int64
	ClosestBytes   int64
	ClosestQuality int
	Iterations     int
}

func (e *TargetSizeError) Error() string {
	switch e.Kind {
	case ErrTargetSizeUnreachable:
		return fmt.Sprintf(
			"imaging: target size %d is unreachable; minimum encoded size is %d bytes",
			e.TargetBytes, e.MinimumBytes,
		)
	case ErrTargetSizeIterationLimit:
		return fmt.Sprintf(
			"imaging: target size %d was not reached within %d iterations; closest size is %d bytes at quality %d",
			e.TargetBytes, e.Iterations, e.ClosestBytes, e.ClosestQuality,
		)
	default:
		return e.Kind.Error()
	}
}

// Unwrap returns the error category.
func (e *TargetSizeError) Unwrap() error {
	return e.Kind
}

// QualitySearchAttempt records one deterministic encoding attempt.
type QualitySearchAttempt struct {
	Iteration    int
	Quality      int
	Parameter    int
	Bytes        int64
	WithinTarget bool
	Duration     time.Duration
	Error        string
}

// QualityEncoderFunc encodes an image once at the supplied quality and returns
// the complete encoded representation.
type QualityEncoderFunc func(quality int) ([]byte, error)

// QualitySearchConfig configures SearchQuality. Its zero value uses qualities
// 1..100, at most 32 encoding attempts, and QualityFirst mode.
type QualitySearchConfig struct {
	Mode          SearchMode
	MinQuality    int
	MaxQuality    int
	MaxIterations int
}

// QualitySearchResult describes a target-size quality search.
type QualitySearchResult struct {
	Data         []byte
	Quality      int
	Parameter    int
	Bytes        int64
	TargetBytes  int64
	Iterations   int
	Attempts     []QualitySearchAttempt
	Duration     time.Duration
	Achieved     bool
	MinimumBytes int64
	Mode         SearchMode
}

// SearchQuality finds an encoding quality for targetBytes using encode. The
// encoder is called at most cfg.MaxIterations times and every call is reflected
// in the returned trace. The search is deterministic and never retries an
// encoder error.
//
// QualityFirst uses a binary search between the lowest and highest qualities,
// relying on the normal monotonic relationship between encoder quality and
// output size. It converges when the fitting and non-fitting qualities are
// adjacent. SizeFirst first checks the lowest quality and returns it when it
// fits.
func SearchQuality(targetBytes int64, encode QualityEncoderFunc, cfg QualitySearchConfig) (*QualitySearchResult, error) {
	start := time.Now()
	cfg, err := normalizeQualitySearchConfig(cfg)
	if err != nil {
		return nil, err
	}
	if targetBytes <= 0 {
		return nil, fmt.Errorf("imaging: target size must be positive")
	}
	if encode == nil {
		return nil, fmt.Errorf("imaging: quality encoder must not be nil")
	}

	search := &qualitySearch{
		target: targetBytes,
		cfg:    cfg,
		encode: encode,
	}
	if cfg.Mode == SizeFirst {
		return search.sizeFirst(start)
	}
	return search.qualityFirst(start)
}

type qualitySearch struct {
	target int64
	cfg    QualitySearchConfig
	encode QualityEncoderFunc

	attempts []QualitySearchAttempt
	fit      *qualityCandidate
	closest  *qualityCandidate
	minSize  int64
}

type qualityCandidate struct {
	quality   int
	parameter int
	size      int64
	data      []byte
}

func (s *qualitySearch) qualityFirst(start time.Time) (*QualitySearchResult, error) {
	maxCandidate, err := s.evaluate(s.cfg.MaxQuality)
	if err != nil {
		return s.errorResult(start, err), err
	}
	if maxCandidate.size <= s.target {
		return s.successResult(start, maxCandidate), nil
	}
	if s.cfg.MinQuality == s.cfg.MaxQuality {
		return s.unreachableResult(start, maxCandidate)
	}
	if s.budgetExhausted() {
		return s.limitResult(start)
	}

	minCandidate, err := s.evaluate(s.cfg.MinQuality)
	if err != nil {
		return s.errorResult(start, err), err
	}
	if minCandidate.size > s.target {
		return s.unreachableResult(start, minCandidate)
	}

	lowFit := minCandidate.quality
	highFail := maxCandidate.quality
	for highFail-lowFit > 1 {
		if s.budgetExhausted() {
			return s.limitResult(start)
		}
		mid := lowFit + (highFail-lowFit)/2
		candidate, err := s.evaluate(mid)
		if err != nil {
			return s.errorResult(start, err), err
		}
		if candidate.size <= s.target {
			lowFit = mid
		} else {
			highFail = mid
		}
	}

	if s.fit == nil {
		return s.limitResult(start)
	}
	return s.successResult(start, *s.fit), nil
}

func (s *qualitySearch) sizeFirst(start time.Time) (*QualitySearchResult, error) {
	candidate, err := s.evaluate(s.cfg.MinQuality)
	if err != nil {
		return s.errorResult(start, err), err
	}
	if candidate.size > s.target {
		return s.unreachableResult(start, candidate)
	}
	return s.successResult(start, candidate), nil
}

func (s *qualitySearch) evaluate(quality int) (qualityCandidate, error) {
	attemptStart := time.Now()
	data, err := s.encode(quality)
	duration := time.Since(attemptStart)
	if err != nil {
		s.attempts = append(s.attempts, QualitySearchAttempt{
			Iteration: len(s.attempts) + 1,
			Quality:   quality,
			Parameter: quality,
			Duration:  duration,
			Error:     err.Error(),
		})
		return qualityCandidate{}, err
	}

	candidate := qualityCandidate{
		quality:   quality,
		parameter: quality,
		size:      int64(len(data)),
		data:      data,
	}
	withinTarget := candidate.size <= s.target
	s.attempts = append(s.attempts, QualitySearchAttempt{
		Iteration:    len(s.attempts) + 1,
		Quality:      quality,
		Parameter:    quality,
		Bytes:        candidate.size,
		WithinTarget: withinTarget,
		Duration:     duration,
	})

	if quality == s.cfg.MinQuality && (s.minSize == 0 || candidate.size < s.minSize) {
		s.minSize = candidate.size
	}
	s.considerClosest(candidate)
	if withinTarget {
		s.considerFit(candidate)
	}
	return candidate, nil
}

func (s *qualitySearch) considerFit(candidate qualityCandidate) {
	if s.fit == nil {
		s.fit = &candidate
		return
	}
	switch s.cfg.Mode {
	case SizeFirst:
		if candidate.size < s.fit.size ||
			(candidate.size == s.fit.size && candidate.quality > s.fit.quality) {
			s.fit = &candidate
		}
	default:
		if candidate.quality > s.fit.quality ||
			(candidate.quality == s.fit.quality && candidate.size < s.fit.size) {
			s.fit = &candidate
		}
	}
}

func (s *qualitySearch) considerClosest(candidate qualityCandidate) {
	if s.closest == nil {
		s.closest = &candidate
		return
	}
	delta := absInt64(candidate.size - s.target)
	closestDelta := absInt64(s.closest.size - s.target)
	if delta < closestDelta ||
		(delta == closestDelta && candidate.size < s.closest.size) {
		s.closest = &candidate
	}
}

func (s *qualitySearch) budgetExhausted() bool {
	return len(s.attempts) >= s.cfg.MaxIterations
}

func (s *qualitySearch) successResult(start time.Time, candidate qualityCandidate) *QualitySearchResult {
	result := s.baseResult(start)
	result.Data = candidate.data
	result.Quality = candidate.quality
	result.Parameter = candidate.parameter
	result.Bytes = candidate.size
	result.Achieved = true
	return result
}

func (s *qualitySearch) unreachableResult(start time.Time, candidate qualityCandidate) (*QualitySearchResult, error) {
	result := s.baseResult(start)
	result.Data = candidate.data
	result.Quality = candidate.quality
	result.Parameter = candidate.parameter
	result.Bytes = candidate.size
	result.MinimumBytes = candidate.size
	return result, &TargetSizeError{
		Kind:           ErrTargetSizeUnreachable,
		TargetBytes:    s.target,
		MinimumBytes:   candidate.size,
		ClosestBytes:   candidate.size,
		ClosestQuality: candidate.quality,
		Iterations:     result.Iterations,
	}
}

func (s *qualitySearch) limitResult(start time.Time) (*QualitySearchResult, error) {
	result := s.baseResult(start)
	candidate := s.closest
	if candidate != nil {
		result.Data = candidate.data
		result.Quality = candidate.quality
		result.Parameter = candidate.parameter
		result.Bytes = candidate.size
	}
	result.MinimumBytes = s.minSize

	var closestBytes int64
	var closestQuality int
	if candidate != nil {
		closestBytes = candidate.size
		closestQuality = candidate.quality
	}
	return result, &TargetSizeError{
		Kind:           ErrTargetSizeIterationLimit,
		TargetBytes:    s.target,
		MinimumBytes:   s.minSize,
		ClosestBytes:   closestBytes,
		ClosestQuality: closestQuality,
		Iterations:     result.Iterations,
	}
}

func (s *qualitySearch) errorResult(start time.Time, _ error) *QualitySearchResult {
	result := s.baseResult(start)
	if s.fit != nil {
		result.Data = s.fit.data
		result.Quality = s.fit.quality
		result.Parameter = s.fit.parameter
		result.Bytes = s.fit.size
	} else if s.closest != nil {
		result.Data = s.closest.data
		result.Quality = s.closest.quality
		result.Parameter = s.closest.parameter
		result.Bytes = s.closest.size
	}
	result.MinimumBytes = s.minSize
	return result
}

func (s *qualitySearch) baseResult(start time.Time) *QualitySearchResult {
	attempts := append([]QualitySearchAttempt(nil), s.attempts...)
	return &QualitySearchResult{
		TargetBytes:  s.target,
		Iterations:   len(attempts),
		Attempts:     attempts,
		Duration:     time.Since(start),
		MinimumBytes: s.minSize,
		Mode:         s.cfg.Mode,
	}
}

func normalizeQualitySearchConfig(cfg QualitySearchConfig) (QualitySearchConfig, error) {
	if cfg.MinQuality == 0 {
		cfg.MinQuality = 1
	}
	if cfg.MaxQuality == 0 {
		cfg.MaxQuality = 100
	}
	if cfg.MaxIterations == 0 {
		cfg.MaxIterations = 32
	}
	if cfg.MinQuality < 1 || cfg.MaxQuality > 100 || cfg.MinQuality > cfg.MaxQuality {
		return cfg, fmt.Errorf("imaging: invalid quality range %d..%d", cfg.MinQuality, cfg.MaxQuality)
	}
	if cfg.MaxIterations < 1 {
		return cfg, fmt.Errorf("imaging: maximum iterations must be positive")
	}
	if cfg.Mode != QualityFirst && cfg.Mode != SizeFirst {
		return cfg, fmt.Errorf("imaging: invalid search mode %d", cfg.Mode)
	}
	return cfg, nil
}

// TargetSizeOption configures OptimizeToTargetSize.
type TargetSizeOption func(*targetSizeConfig)

// TargetSizeImageEncoderFunc encodes a decoded image at a normalized quality.
// It is primarily useful for deterministic tests without large images.
type TargetSizeImageEncoderFunc func(img image.Image, quality int) ([]byte, error)

// TargetSizeEncoderOption configures a TargetSizeEncoder.
type TargetSizeEncoderOption func(*TargetSizeEncoder)

// TargetSizeAttempt records one encoding attempt in a target-size search.
type TargetSizeAttempt = QualitySearchAttempt

// TargetSizeResult is the result of OptimizeToTargetSize. Iterations counts
// work performed by the current call. When Cached or Merged is true it is zero,
// while Attempts retains the trace of the search that produced the parameters.
//
// MinimumBytes is the smallest measured output when a search exhausts the
// lossless parameter set or evaluates the lowest lossy quality. It is zero when
// that lower bound was not needed.
type TargetSizeResult struct {
	Data           []byte
	Format         Format
	SourceFormat   Format
	Quality        int
	Parameter      int
	Bytes          int64
	TargetBytes    int64
	Iterations     int
	Attempts       []TargetSizeAttempt
	Duration       time.Duration
	Achieved       bool
	Unchanged      bool
	Cached         bool
	Merged         bool
	MinimumBytes   int64
	Mode           SearchMode
	Alpha          bool
	AlphaFlattened bool
}

// TargetSizeEncoder owns an independent cache and merges concurrent searches
// for the same source bytes and target. Its zero value is usable.
type TargetSizeEncoder struct {
	mu            sync.Mutex
	cache         map[[sha256.Size]byte]*targetSizeCacheEntry
	cacheOrder    [][sha256.Size]byte
	inflight      map[[sha256.Size]byte]*targetSizeFlight
	cacheCapacity int
	cacheDisabled bool
	initialized   bool
	imageEncoder  TargetSizeImageEncoderFunc
}

type targetSizeCacheEntry struct {
	result *TargetSizeResult
}

type targetSizeFlight struct {
	done   chan struct{}
	result *TargetSizeResult
	err    error
}

type targetSizeConfig struct {
	format          Format
	formatSet       bool
	mode            SearchMode
	minQuality      int
	maxQuality      int
	maxIterations   int
	alphaBackground color.NRGBA
}

func defaultTargetSizeConfig() targetSizeConfig {
	return targetSizeConfig{
		mode:            QualityFirst,
		minQuality:      1,
		maxQuality:      100,
		maxIterations:   32,
		alphaBackground: color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff},
	}
}

// TargetSizeFormat sets the output format. By default the source format is
// retained. If the format differs from the source, the source bytes cannot be
// passed through unchanged.
func TargetSizeFormat(format Format) TargetSizeOption {
	return func(cfg *targetSizeConfig) {
		cfg.format = format
		cfg.formatSet = true
	}
}

// TargetSizeSearchMode selects QualityFirst (the default) or SizeFirst.
func TargetSizeSearchMode(mode SearchMode) TargetSizeOption {
	return func(cfg *targetSizeConfig) {
		cfg.mode = mode
	}
}

// TargetSizeQualityRange limits the quality values considered by the search.
func TargetSizeQualityRange(minQuality, maxQuality int) TargetSizeOption {
	return func(cfg *targetSizeConfig) {
		cfg.minQuality = minQuality
		cfg.maxQuality = maxQuality
	}
}

// TargetSizeMaxIterations caps the number of encoding attempts.
func TargetSizeMaxIterations(maxIterations int) TargetSizeOption {
	return func(cfg *targetSizeConfig) {
		cfg.maxIterations = maxIterations
	}
}

// TargetSizeAlphaBackground sets the opaque background used when a JPEG output
// is produced from an image with transparency. The default is white.
func TargetSizeAlphaBackground(background color.Color) TargetSizeOption {
	return func(cfg *targetSizeConfig) {
		if background != nil {
			cfg.alphaBackground = opaqueNRGBA(background)
		}
	}
}

// WithTargetSizeImageEncoder replaces the existing encoder used for lossy
// searches. The callback receives the decoded image and normalized quality.
func WithTargetSizeImageEncoder(encode TargetSizeImageEncoderFunc) TargetSizeEncoderOption {
	return func(encoder *TargetSizeEncoder) {
		encoder.imageEncoder = encode
	}
}

// WithTargetSizeCacheEntries sets the maximum cache size. A non-positive value
// disables the cache.
func WithTargetSizeCacheEntries(entries int) TargetSizeEncoderOption {
	return func(encoder *TargetSizeEncoder) {
		encoder.cacheCapacity = entries
		encoder.cacheDisabled = entries <= 0
	}
}

// NewTargetSizeEncoder returns an isolated encoder with its own cache.
func NewTargetSizeEncoder(opts ...TargetSizeEncoderOption) *TargetSizeEncoder {
	encoder := &TargetSizeEncoder{cacheCapacity: 128}
	for _, option := range opts {
		option(encoder)
	}
	encoder.init()
	return encoder
}

var defaultTargetSizeEncoder = NewTargetSizeEncoder()

// OptimizeToTargetSize searches for an encoding no larger than targetBytes
// using a process-wide cache. Use NewTargetSizeEncoder for isolated state.
func OptimizeToTargetSize(data []byte, targetBytes int64, opts ...TargetSizeOption) (*TargetSizeResult, error) {
	return defaultTargetSizeEncoder.OptimizeToTargetSize(data, targetBytes, opts...)
}

// EncodeToTargetSize is an alias for OptimizeToTargetSize.
func EncodeToTargetSize(data []byte, targetBytes int64, opts ...TargetSizeOption) (*TargetSizeResult, error) {
	return OptimizeToTargetSize(data, targetBytes, opts...)
}

// OptimizeToTargetSize searches for an encoding no larger than targetBytes.
//
// Source bytes already within the limit are returned unchanged without an
// encode. Lossless output formats are never converted to a lossy format: when
// even the smallest lossless encoding exceeds the target, the returned error
// wraps ErrTargetSizeUnreachable and MinimumBytes reports the observed lower
// bound. A failed iteration-limited search returns its closest candidate and
// wraps ErrTargetSizeIterationLimit.
func (e *TargetSizeEncoder) OptimizeToTargetSize(data []byte, targetBytes int64, opts ...TargetSizeOption) (*TargetSizeResult, error) {
	start := time.Now()
	if len(data) == 0 {
		return nil, fmt.Errorf("imaging: source image must not be empty")
	}
	if targetBytes <= 0 {
		return nil, fmt.Errorf("imaging: target size must be positive")
	}

	cfg := defaultTargetSizeConfig()
	for _, option := range opts {
		option(&cfg)
	}
	normalized, err := normalizeQualitySearchConfig(QualitySearchConfig{
		Mode:          cfg.mode,
		MinQuality:    cfg.minQuality,
		MaxQuality:    cfg.maxQuality,
		MaxIterations: cfg.maxIterations,
	})
	if err != nil {
		return nil, err
	}
	cfg.mode = normalized.Mode
	cfg.minQuality = normalized.MinQuality
	cfg.maxQuality = normalized.MaxQuality
	cfg.maxIterations = normalized.MaxIterations

	sourceFormat, err := detectSourceFormat(data)
	if err != nil {
		return nil, err
	}
	outputFormat := sourceFormat
	if cfg.formatSet {
		if !knownFormat(cfg.format) {
			return nil, ErrUnsupportedFormat
		}
		outputFormat = cfg.format
	}

	if outputFormat == sourceFormat && int64(len(data)) <= targetBytes {
		return &TargetSizeResult{
			Data:         append([]byte(nil), data...),
			Format:       outputFormat,
			SourceFormat: sourceFormat,
			Bytes:        int64(len(data)),
			TargetBytes:  targetBytes,
			Duration:     time.Since(start),
			Achieved:     true,
			Unchanged:    true,
			MinimumBytes: int64(len(data)),
			Mode:         cfg.mode,
		}, nil
	}

	e.init()
	key := targetSizeCacheKey(data, outputFormat, targetBytes, cfg)
	e.mu.Lock()
	if !e.cacheDisabled {
		if entry := e.cache[key]; entry != nil {
			result := cloneTargetSizeResult(entry.result)
			result.Duration = time.Since(start)
			result.Iterations = 0
			result.Cached = true
			result.Merged = false
			e.mu.Unlock()
			return result, nil
		}
	}
	if flight := e.inflight[key]; flight != nil {
		e.mu.Unlock()
		<-flight.done
		result := cloneTargetSizeResult(flight.result)
		if result != nil {
			result.Duration = time.Since(start)
			result.Iterations = 0
			result.Cached = false
			result.Merged = true
		}
		return result, flight.err
	}
	flight := &targetSizeFlight{done: make(chan struct{})}
	e.inflight[key] = flight
	e.mu.Unlock()

	result, searchErr := e.optimize(data, sourceFormat, outputFormat, targetBytes, cfg, start)

	e.mu.Lock()
	if searchErr == nil {
		e.putCacheLocked(key, cloneTargetSizeResult(result))
	}
	flight.result = cloneTargetSizeResult(result)
	flight.err = searchErr
	delete(e.inflight, key)
	close(flight.done)
	e.mu.Unlock()

	return result, searchErr
}

func (e *TargetSizeEncoder) optimize(
	data []byte,
	sourceFormat Format,
	outputFormat Format,
	targetBytes int64,
	cfg targetSizeConfig,
	start time.Time,
) (*TargetSizeResult, error) {
	img, err := Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	alpha := imageHasAlpha(img)
	alphaFlattened := alpha && outputFormat == JPEG
	baseImage := img
	if alphaFlattened {
		baseImage = flattenOnBackground(img, cfg.alphaBackground)
	}

	if isLosslessFormat(outputFormat) {
		return e.searchLossless(
			baseImage, sourceFormat, outputFormat, targetBytes, cfg, alpha, alphaFlattened, start,
		)
	}

	encode := func(quality int) ([]byte, error) {
		if e.imageEncoder != nil {
			return e.imageEncoder(baseImage, quality)
		}
		parameter := quality
		if outputFormat == GIF {
			parameter = qualityToColors(quality)
		}
		return encodeTargetImage(baseImage, outputFormat, parameter)
	}
	searchResult, searchErr := SearchQuality(targetBytes, encode, QualitySearchConfig{
		Mode:          cfg.mode,
		MinQuality:    cfg.minQuality,
		MaxQuality:    cfg.maxQuality,
		MaxIterations: cfg.maxIterations,
	})
	if searchResult == nil {
		return nil, searchErr
	}

	result := &TargetSizeResult{
		Data:           searchResult.Data,
		Format:         outputFormat,
		SourceFormat:   sourceFormat,
		Quality:        searchResult.Quality,
		Parameter:      searchResult.Parameter,
		Bytes:          searchResult.Bytes,
		TargetBytes:    targetBytes,
		Iterations:     searchResult.Iterations,
		Attempts:       searchResult.Attempts,
		Duration:       time.Since(start),
		Achieved:       searchResult.Achieved,
		MinimumBytes:   searchResult.MinimumBytes,
		Mode:           cfg.mode,
		Alpha:          alpha,
		AlphaFlattened: alphaFlattened,
	}
	return result, searchErr
}

func (e *TargetSizeEncoder) searchLossless(
	img image.Image,
	sourceFormat Format,
	outputFormat Format,
	targetBytes int64,
	cfg targetSizeConfig,
	alpha bool,
	alphaFlattened bool,
	start time.Time,
) (*TargetSizeResult, error) {
	parameters := []int{0}
	if outputFormat == PNG {
		parameters = []int{
			int(png.BestCompression),
			int(png.DefaultCompression),
			int(png.BestSpeed),
			int(png.NoCompression),
		}
	}

	attempts := make([]TargetSizeAttempt, 0, len(parameters))
	var smallest *qualityCandidate
	var closest *qualityCandidate
	for _, parameter := range parameters {
		if len(attempts) >= cfg.maxIterations {
			break
		}
		attemptStart := time.Now()
		data, err := encodeTargetImage(img, outputFormat, parameter)
		duration := time.Since(attemptStart)
		if err != nil {
			attempts = append(attempts, TargetSizeAttempt{
				Iteration: len(attempts) + 1,
				Parameter: parameter,
				Duration:  duration,
				Error:     err.Error(),
			})
			return &TargetSizeResult{
				Format:         outputFormat,
				SourceFormat:   sourceFormat,
				Parameter:      parameter,
				TargetBytes:    targetBytes,
				Iterations:     len(attempts),
				Attempts:       append([]TargetSizeAttempt(nil), attempts...),
				Duration:       time.Since(start),
				Mode:           cfg.mode,
				Alpha:          alpha,
				AlphaFlattened: alphaFlattened,
			}, err
		}

		size := int64(len(data))
		withinTarget := size <= targetBytes
		attempts = append(attempts, TargetSizeAttempt{
			Iteration:    len(attempts) + 1,
			Parameter:    parameter,
			Bytes:        size,
			WithinTarget: withinTarget,
			Duration:     duration,
		})
		candidate := qualityCandidate{parameter: parameter, size: size, data: data}
		if smallest == nil || size < smallest.size {
			smallest = &candidate
		}
		if closest == nil || absInt64(size-targetBytes) < absInt64(closest.size-targetBytes) ||
			(absInt64(size-targetBytes) == absInt64(closest.size-targetBytes) && size < closest.size) {
			closest = &candidate
		}
		if withinTarget {
			return &TargetSizeResult{
				Data:           data,
				Format:         outputFormat,
				SourceFormat:   sourceFormat,
				Parameter:      parameter,
				Bytes:          size,
				TargetBytes:    targetBytes,
				Iterations:     len(attempts),
				Attempts:       append([]TargetSizeAttempt(nil), attempts...),
				Duration:       time.Since(start),
				Achieved:       true,
				MinimumBytes:   size,
				Mode:           cfg.mode,
				Alpha:          alpha,
				AlphaFlattened: alphaFlattened,
			}, nil
		}
	}

	result := &TargetSizeResult{
		Format:         outputFormat,
		SourceFormat:   sourceFormat,
		TargetBytes:    targetBytes,
		Iterations:     len(attempts),
		Attempts:       append([]TargetSizeAttempt(nil), attempts...),
		Duration:       time.Since(start),
		Mode:           cfg.mode,
		Alpha:          alpha,
		AlphaFlattened: alphaFlattened,
	}
	if len(attempts) < len(parameters) {
		if closest != nil {
			result.Data = closest.data
			result.Parameter = closest.parameter
			result.Bytes = closest.size
		}
		if smallest != nil {
			result.MinimumBytes = smallest.size
		}
		return result, &TargetSizeError{
			Kind:           ErrTargetSizeIterationLimit,
			TargetBytes:    targetBytes,
			MinimumBytes:   result.MinimumBytes,
			ClosestBytes:   result.Bytes,
			ClosestQuality: 0,
			Iterations:     len(attempts),
		}
	}
	if smallest != nil {
		result.Data = smallest.data
		result.Parameter = smallest.parameter
		result.Bytes = smallest.size
		result.MinimumBytes = smallest.size
	}
	return result, &TargetSizeError{
		Kind:           ErrTargetSizeUnreachable,
		TargetBytes:    targetBytes,
		MinimumBytes:   result.MinimumBytes,
		ClosestBytes:   result.Bytes,
		ClosestQuality: 0,
		Iterations:     len(attempts),
	}
}

func (e *TargetSizeEncoder) init() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.initialized {
		return
	}
	e.cache = make(map[[sha256.Size]byte]*targetSizeCacheEntry)
	e.inflight = make(map[[sha256.Size]byte]*targetSizeFlight)
	if e.cacheCapacity == 0 && !e.cacheDisabled {
		e.cacheCapacity = 128
	}
	e.initialized = true
}

func (e *TargetSizeEncoder) putCacheLocked(key [sha256.Size]byte, result *TargetSizeResult) {
	if e.cacheDisabled || e.cacheCapacity <= 0 || result == nil {
		return
	}
	if _, exists := e.cache[key]; exists {
		return
	}
	for len(e.cache) >= e.cacheCapacity && len(e.cacheOrder) > 0 {
		oldest := e.cacheOrder[0]
		e.cacheOrder = e.cacheOrder[1:]
		delete(e.cache, oldest)
	}
	e.cache[key] = &targetSizeCacheEntry{result: result}
	e.cacheOrder = append(e.cacheOrder, key)
}

func targetSizeCacheKey(
	data []byte,
	format Format,
	targetBytes int64,
	cfg targetSizeConfig,
) [sha256.Size]byte {
	hash := sha256.New()
	_, _ = hash.Write(data)
	var buffer [8]byte
	writeUint64 := func(value uint64) {
		binary.LittleEndian.PutUint64(buffer[:], value)
		_, _ = hash.Write(buffer[:])
	}
	writeUint64(uint64(targetBytes))
	writeUint64(uint64(format))
	writeUint64(uint64(cfg.mode))
	writeUint64(uint64(cfg.minQuality))
	writeUint64(uint64(cfg.maxQuality))
	writeUint64(uint64(cfg.maxIterations))
	_, _ = hash.Write([]byte{
		cfg.alphaBackground.R,
		cfg.alphaBackground.G,
		cfg.alphaBackground.B,
		cfg.alphaBackground.A,
	})
	var key [sha256.Size]byte
	copy(key[:], hash.Sum(nil))
	return key
}

func cloneTargetSizeResult(result *TargetSizeResult) *TargetSizeResult {
	if result == nil {
		return nil
	}
	clone := *result
	clone.Data = append([]byte(nil), result.Data...)
	clone.Attempts = append([]TargetSizeAttempt(nil), result.Attempts...)
	return &clone
}

func detectSourceFormat(data []byte) (Format, error) {
	_, formatName, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return -1, fmt.Errorf("imaging: decode image config: %w", err)
	}
	switch formatName {
	case "jpeg":
		return JPEG, nil
	case "png":
		return PNG, nil
	case "gif":
		return GIF, nil
	case "tiff":
		return TIFF, nil
	case "bmp":
		return BMP, nil
	default:
		return -1, ErrUnsupportedFormat
	}
}

func knownFormat(format Format) bool {
	switch format {
	case JPEG, PNG, GIF, TIFF, BMP:
		return true
	default:
		return false
	}
}

func isLosslessFormat(format Format) bool {
	switch format {
	case PNG, TIFF, BMP:
		return true
	default:
		return false
	}
}

func encodeTargetImage(img image.Image, format Format, parameter int) ([]byte, error) {
	var buf bytes.Buffer
	var opts []EncodeOption
	switch format {
	case JPEG:
		opts = append(opts, JPEGQuality(parameter))
	case PNG:
		opts = append(opts, PNGCompressionLevel(png.CompressionLevel(parameter)))
	case GIF:
		opts = append(opts, GIFNumColors(parameter))
	case TIFF, BMP:
	default:
		return nil, ErrUnsupportedFormat
	}
	if err := Encode(&buf, img, format, opts...); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func qualityToColors(quality int) int {
	if quality <= 1 {
		return 1
	}
	if quality >= 100 {
		return 256
	}
	return 1 + (quality-1)*255/99
}

func imageHasAlpha(img image.Image) bool {
	switch img.(type) {
	case *image.YCbCr, *image.Gray, *image.Gray16, *image.CMYK:
		return false
	}
	type opaqueImage interface {
		Opaque() bool
	}
	if opaque, ok := img.(opaqueImage); ok {
		return !opaque.Opaque()
	}
	bounds := img.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			if color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA).A != 0xff {
				return true
			}
		}
	}
	return false
}

func flattenOnBackground(img image.Image, background color.NRGBA) *image.NRGBA {
	bounds := img.Bounds()
	dst := image.NewNRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	draw.Draw(dst, dst.Bounds(), &image.Uniform{C: background}, image.Point{}, draw.Src)
	draw.Draw(dst, dst.Bounds(), img, bounds.Min, draw.Over)
	return dst
}

func opaqueNRGBA(value color.Color) color.NRGBA {
	nrgba := color.NRGBAModel.Convert(value).(color.NRGBA)
	nrgba.A = 0xff
	return nrgba
}

func absInt64(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}
