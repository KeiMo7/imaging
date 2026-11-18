package imaging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"io"
	"os"
	"runtime"
	"sync"
	"time"
)

const (
	// DefaultBatchConcurrency is the upper bound for the concurrency selected
	// when an invalid value is supplied in BatchOptions.
	DefaultBatchConcurrency = 4

	// DefaultBatchSize is the maximum number of inputs dispatched as one
	// scheduling batch when an invalid value is supplied in BatchOptions.
	DefaultBatchSize = 32

	// DefaultBatchMaxBytes is the maximum total input size in one scheduling
	// batch when an invalid value is supplied in BatchOptions.
	DefaultBatchMaxBytes int64 = 64 << 20
)

var (
	// ErrInvalidBatchParams is returned when batch processing parameters are
	// invalid. The batch is not started when this error is returned.
	ErrInvalidBatchParams = errors.New("imaging: invalid batch parameters")

	// ErrBatchCanceled is recorded for inputs that were not started because a
	// batch was canceled.
	ErrBatchCanceled = errors.New("imaging: batch canceled")
)

// BatchOperation specifies the operation applied to every input in a batch.
type BatchOperation int

const (
	// BatchResize resizes an image. Width or height may be zero while the other
	// dimension is non-zero to preserve the aspect ratio.
	BatchResize BatchOperation = iota

	// BatchFit scales an image down to fit within Width x Height.
	BatchFit

	// BatchFill resizes and crops an image to exactly Width x Height.
	BatchFill

	// BatchThumbnail resizes and center-crops an image to Width x Height.
	BatchThumbnail

	// BatchOriginal decodes and re-encodes an image without changing its pixels.
	BatchOriginal
)

// Aliases with explicit operation names keep call sites self-documenting.
const (
	BatchOperationResize    = BatchResize
	BatchOperationFit       = BatchFit
	BatchOperationFill      = BatchFill
	BatchOperationThumbnail = BatchThumbnail
	BatchOperationOriginal  = BatchOriginal
)

// BatchInput describes one input/output pair in a batch.
//
// Source is the original filename and is recorded in every result and failure
// report. Destination determines the output format from its extension, using
// the same rules as Save.
type BatchInput struct {
	Source      string
	Destination string
}

// BatchParams contains the processing parameters shared by every input in a
// batch.
//
// Process may be set to use a custom image operation. When Process is non-nil,
// Operation, Width, Height, Filter and Anchor are ignored. Process can run
// concurrently and must be safe for concurrent use. Validate is called exactly
// once before any input is opened.
type BatchParams struct {
	Operation BatchOperation
	Width     int
	Height    int
	Filter    ResampleFilter
	Anchor    Anchor

	Process  func(image.Image) (*image.NRGBA, error)
	Validate func() error

	DecodeOptions []DecodeOption
	EncodeOptions []EncodeOption
}

// BatchOptions controls scheduling and progress reporting.
//
// Concurrency limits the number of images processed at the same time. Values
// less than or equal to zero are replaced with min(runtime.GOMAXPROCS(0),
// DefaultBatchConcurrency) and a warning is included in BatchResult.Warnings.
//
// BatchSize and MaxBatchBytes split inputs into bounded scheduling batches.
// Batching limits the number of tasks queued in front of the workers; it does
// not retain decoded image data. A single input larger than MaxBatchBytes is
// placed in its own batch. Zero selects the defaults for BatchSize and
// MaxBatchBytes.
type BatchOptions struct {
	Concurrency   int
	BatchSize     int
	MaxBatchBytes int64
	Progress      func(BatchProgress)
	Context       context.Context
}

// BatchStatus is the terminal state of one batch item.
type BatchStatus int

const (
	// BatchStatusSucceeded means the image was decoded, processed and saved.
	BatchStatusSucceeded BatchStatus = iota

	// BatchStatusFailed means the item failed. Err contains the item-specific
	// error, including the original filename.
	BatchStatusFailed

	// BatchStatusCanceled means the item was never started because the batch
	// was canceled.
	BatchStatusCanceled
)

// BatchItemResult is the result for one input. Items always appear in input
// order, regardless of completion order.
type BatchItemResult struct {
	Index       int
	Source      string
	Destination string
	Status      BatchStatus
	Err         error
	Duration    time.Duration
}

// BatchProgress is a point-in-time snapshot of batch progress.
//
// Completed includes successful, failed and canceled items. Active is the
// number of items currently occupying a worker. Concurrency is the configured
// maximum concurrency.
type BatchProgress struct {
	Total       int
	Completed   int
	Succeeded   int
	Failed      int
	Canceled    int
	Active      int
	Concurrency int
	Batch       int
	Batches     int
}

// BatchFailure is the JSON representation written by WriteFailureList.
type BatchFailure struct {
	Index       int    `json:"index"`
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Error       string `json:"error"`
}

// BatchResult is the complete result of a batch. A partially failed batch has
// a nil top-level error; item failures are available in Failed and Items.
type BatchResult struct {
	Total       int
	Items       []BatchItemResult
	Succeeded   []BatchItemResult
	Failed      []BatchItemResult
	Canceled    []BatchItemResult
	Warnings    []string
	WasCanceled bool
	StartedAt   time.Time
	FinishedAt  time.Time
}

// RetryInputs returns the failed inputs in input order. The result can be
// passed directly to another batch call.
func (r BatchResult) RetryInputs() []BatchInput {
	inputs := make([]BatchInput, 0, len(r.Failed))
	for _, item := range r.Failed {
		inputs = append(inputs, BatchInput{
			Source:      item.Source,
			Destination: item.Destination,
		})
	}
	return inputs
}

// HasFailures reports whether any item failed.
func (r BatchResult) HasFailures() bool {
	return len(r.Failed) > 0
}

// FailureRecords returns structured failure information in input order.
func (r BatchResult) FailureRecords() []BatchFailure {
	records := make([]BatchFailure, 0, len(r.Failed))
	for _, item := range r.Failed {
		message := ""
		if item.Err != nil {
			message = item.Err.Error()
		}
		records = append(records, BatchFailure{
			Index:       item.Index,
			Source:      item.Source,
			Destination: item.Destination,
			Error:       message,
		})
	}
	return records
}

// WriteFailureList writes failed inputs as indented JSON. The list contains the
// original filename, destination and error for each failed item.
func (r BatchResult) WriteFailureList(filename string) error {
	file, err := fs.Create(filename)
	if err != nil {
		return err
	}
	err = r.WriteFailureListTo(file)
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	return err
}

// WriteFailureListTo writes failed inputs as indented JSON to w.
func (r BatchResult) WriteFailureListTo(w io.Writer) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(r.FailureRecords())
}

// BatchJob represents an asynchronous batch.
type BatchJob struct {
	ctx    context.Context
	cancel context.CancelFunc

	inputs  []BatchInput
	params  BatchParams
	options batchRuntimeOptions
	chunks  []batchChunk

	done     chan struct{}
	mu       sync.Mutex
	progress BatchProgress
	result   BatchResult
	recorded []bool
}

type batchRuntimeOptions struct {
	concurrency   int
	batchSize     int
	maxBatchBytes int64
	progress      func(BatchProgress)
}

type batchChunk struct {
	indices []int
	bytes   int64
}

type batchCompletion struct {
	index  int
	result BatchItemResult
}

// ProcessBatch runs a batch synchronously. The returned error is non-nil only
// when the batch cannot start because its parameters are invalid.
func ProcessBatch(inputs []BatchInput, params BatchParams, opts BatchOptions) (BatchResult, error) {
	return ProcessBatchContext(nil, inputs, params, opts)
}

// ProcessBatchContext runs a batch synchronously with ctx. Cancellation has
// graceful semantics: no new items are started, items already running are
// allowed to finish, and the remaining inputs are returned as canceled.
func ProcessBatchContext(ctx context.Context, inputs []BatchInput, params BatchParams, opts BatchOptions) (BatchResult, error) {
	job, err := ProcessBatchAsyncContext(ctx, inputs, params, opts)
	if err != nil {
		return BatchResult{}, err
	}
	return job.Wait(), nil
}

// ProcessBatchAsync starts a batch and returns immediately. The returned job
// provides Wait, Cancel, Done and Progress.
func ProcessBatchAsync(inputs []BatchInput, params BatchParams, opts BatchOptions) (*BatchJob, error) {
	return ProcessBatchAsyncContext(nil, inputs, params, opts)
}

// ProcessBatchAsyncContext starts a batch with ctx and returns immediately.
func ProcessBatchAsyncContext(ctx context.Context, inputs []BatchInput, params BatchParams, opts BatchOptions) (*BatchJob, error) {
	if ctx == nil {
		ctx = opts.Context
	}
	if ctx == nil {
		ctx = context.Background()
	}

	if err := validateBatchParams(params); err != nil {
		return nil, err
	}

	runtimeOpts, warnings := normalizeBatchOptions(opts, len(inputs))
	jobCtx, cancel := context.WithCancel(ctx)
	job := &BatchJob{
		ctx:     jobCtx,
		cancel:  cancel,
		inputs:  append([]BatchInput(nil), inputs...),
		params:  copyBatchParams(params),
		options: runtimeOpts,
		done:    make(chan struct{}),
	}
	job.chunks = buildBatchChunks(job.inputs, runtimeOpts)
	job.progress = BatchProgress{
		Total:       len(job.inputs),
		Concurrency: runtimeOpts.concurrency,
		Batches:     len(job.chunks),
	}
	job.result = BatchResult{
		Total:     len(job.inputs),
		Items:     make([]BatchItemResult, len(job.inputs)),
		Warnings:  append([]string(nil), warnings...),
		StartedAt: time.Now(),
	}
	job.recorded = make([]bool, len(job.inputs))
	for i, input := range job.inputs {
		job.result.Items[i] = BatchItemResult{
			Index:       i,
			Source:      input.Source,
			Destination: input.Destination,
		}
	}

	go job.run()
	return job, nil
}

// BatchProcess is an alias for ProcessBatch.
func BatchProcess(inputs []BatchInput, params BatchParams, opts BatchOptions) (BatchResult, error) {
	return ProcessBatch(inputs, params, opts)
}

// BatchProcessAsync is an alias for ProcessBatchAsync.
func BatchProcessAsync(inputs []BatchInput, params BatchParams, opts BatchOptions) (*BatchJob, error) {
	return ProcessBatchAsync(inputs, params, opts)
}

// BatchProcessContext is an alias for ProcessBatchContext.
func BatchProcessContext(ctx context.Context, inputs []BatchInput, params BatchParams, opts BatchOptions) (BatchResult, error) {
	return ProcessBatchContext(ctx, inputs, params, opts)
}

// BatchProcessAsyncContext is an alias for ProcessBatchAsyncContext.
func BatchProcessAsyncContext(ctx context.Context, inputs []BatchInput, params BatchParams, opts BatchOptions) (*BatchJob, error) {
	return ProcessBatchAsyncContext(ctx, inputs, params, opts)
}

// Done is closed when the batch has finished.
func (j *BatchJob) Done() <-chan struct{} {
	return j.done
}

// Wait blocks until the batch finishes and returns all per-item results.
func (j *BatchJob) Wait() BatchResult {
	<-j.done
	j.mu.Lock()
	defer j.mu.Unlock()
	return cloneBatchResult(j.result)
}

// Result waits for the batch to finish and returns its result.
func (j *BatchJob) Result() BatchResult {
	return j.Wait()
}

// Cancel requests graceful cancellation. Items already running are allowed to
// finish; items not yet dispatched are marked BatchStatusCanceled.
func (j *BatchJob) Cancel() {
	j.cancel()
}

// Progress returns a concurrency-safe snapshot of batch progress.
func (j *BatchJob) Progress() BatchProgress {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.progress
}

// Warnings returns non-fatal configuration warnings such as an invalid
// concurrency fallback.
func (j *BatchJob) Warnings() []string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]string(nil), j.result.Warnings...)
}

func copyBatchParams(params BatchParams) BatchParams {
	params.DecodeOptions = append([]DecodeOption(nil), params.DecodeOptions...)
	params.EncodeOptions = append([]EncodeOption(nil), params.EncodeOptions...)
	return params
}

func validateBatchParams(params BatchParams) error {
	if params.Validate != nil {
		if err := params.Validate(); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidBatchParams, err)
		}
	}

	if params.Process != nil {
		return nil
	}

	switch params.Operation {
	case BatchResize:
		if params.Width < 0 || params.Height < 0 {
			return fmt.Errorf("%w: resize dimensions must be non-negative", ErrInvalidBatchParams)
		}
		if params.Width == 0 && params.Height == 0 {
			return fmt.Errorf("%w: resize width and height cannot both be zero", ErrInvalidBatchParams)
		}
	case BatchFit, BatchFill, BatchThumbnail:
		if params.Width <= 0 || params.Height <= 0 {
			return fmt.Errorf("%w: width and height must be positive", ErrInvalidBatchParams)
		}
	case BatchOriginal:
		return nil
	default:
		return fmt.Errorf("%w: unknown operation %d", ErrInvalidBatchParams, params.Operation)
	}

	if params.Width > 0 && params.Height > 0 {
		maxInt := int(^uint(0) >> 1)
		if params.Width > (maxInt/4)/params.Height {
			return fmt.Errorf("%w: output dimensions overflow", ErrInvalidBatchParams)
		}
	}
	if params.Filter.Support < 0 || (params.Filter.Support > 0 && params.Filter.Kernel == nil) {
		return fmt.Errorf("%w: invalid resample filter", ErrInvalidBatchParams)
	}
	if params.Operation == BatchFill || params.Operation == BatchThumbnail {
		if params.Anchor < Center || params.Anchor > BottomRight {
			return fmt.Errorf("%w: invalid anchor %d", ErrInvalidBatchParams, params.Anchor)
		}
	}
	return nil
}

func defaultBatchConcurrency() int {
	concurrency := runtime.GOMAXPROCS(0)
	if concurrency > DefaultBatchConcurrency {
		concurrency = DefaultBatchConcurrency
	}
	if concurrency < 1 {
		concurrency = 1
	}
	return concurrency
}

func normalizeBatchOptions(opts BatchOptions, total int) (batchRuntimeOptions, []string) {
	runtimeOpts := batchRuntimeOptions{
		concurrency:   opts.Concurrency,
		batchSize:     opts.BatchSize,
		maxBatchBytes: opts.MaxBatchBytes,
		progress:      opts.Progress,
	}
	var warnings []string

	if runtimeOpts.concurrency <= 0 {
		runtimeOpts.concurrency = defaultBatchConcurrency()
		warnings = append(warnings, fmt.Sprintf(
			"imaging: invalid batch concurrency %d; using default %d",
			opts.Concurrency, runtimeOpts.concurrency,
		))
	}
	if total > 0 && runtimeOpts.concurrency > total {
		runtimeOpts.concurrency = total
	}
	if runtimeOpts.batchSize == 0 {
		runtimeOpts.batchSize = DefaultBatchSize
	} else if runtimeOpts.batchSize < 0 {
		runtimeOpts.batchSize = DefaultBatchSize
		warnings = append(warnings, fmt.Sprintf(
			"imaging: invalid batch size %d; using default %d",
			opts.BatchSize, DefaultBatchSize,
		))
	}
	if runtimeOpts.maxBatchBytes == 0 {
		runtimeOpts.maxBatchBytes = DefaultBatchMaxBytes
	} else if runtimeOpts.maxBatchBytes < 0 {
		runtimeOpts.maxBatchBytes = DefaultBatchMaxBytes
		warnings = append(warnings, fmt.Sprintf(
			"imaging: invalid max batch bytes %d; using default %d",
			opts.MaxBatchBytes, DefaultBatchMaxBytes,
		))
	}
	return runtimeOpts, warnings
}

func buildBatchChunks(inputs []BatchInput, opts batchRuntimeOptions) []batchChunk {
	if len(inputs) == 0 {
		return nil
	}

	chunks := make([]batchChunk, 0, (len(inputs)+opts.batchSize-1)/opts.batchSize)
	current := batchChunk{}
	flush := func() {
		if len(current.indices) == 0 {
			return
		}
		chunks = append(chunks, current)
		current = batchChunk{}
	}

	for i, input := range inputs {
		size := batchInputSize(input.Source)
		if len(current.indices) > 0 {
			if len(current.indices) >= opts.batchSize ||
				(current.bytes > 0 && size > 0 && current.bytes+size > opts.maxBatchBytes) {
				flush()
			}
		}
		current.indices = append(current.indices, i)
		current.bytes += size
		if len(current.indices) >= opts.batchSize ||
			(current.bytes > 0 && current.bytes >= opts.maxBatchBytes) {
			flush()
		}
	}
	flush()
	return chunks
}

func batchInputSize(filename string) int64 {
	if filename == "" {
		return 0
	}
	statter, ok := fs.(interface {
		Stat(string) (os.FileInfo, error)
	})
	if !ok {
		return 0
	}
	info, err := statter.Stat(filename)
	if err != nil {
		return 0
	}
	return info.Size()
}

func (j *BatchJob) run() {
	defer close(j.done)
	defer j.cancel()
	defer func() {
		j.mu.Lock()
		j.result.FinishedAt = time.Now()
		j.mu.Unlock()
	}()

	j.emitProgress()

	jobs := make(chan int)
	completions := make(chan batchCompletion, j.options.concurrency)
	var workers sync.WaitGroup
	for i := 0; i < j.options.concurrency; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				started := time.Now()
				result := j.processItem(index)
				result.Duration = time.Since(started)
				completions <- batchCompletion{index: index, result: result}
			}
		}()
	}

	active := 0
	canceled := false
	for batchIndex, chunk := range j.chunks {
		if j.ctx.Err() != nil {
			canceled = true
			for _, later := range j.chunks[batchIndex:] {
				for _, index := range later.indices {
					j.markCanceled(index)
				}
			}
			break
		}

		j.setCurrentBatch(batchIndex + 1)
		if j.runChunk(chunk, jobs, completions, &active) {
			canceled = true
			for _, later := range j.chunks[batchIndex+1:] {
				for _, index := range later.indices {
					j.markCanceled(index)
				}
			}
			break
		}
	}

	close(jobs)
	workers.Wait()

	j.mu.Lock()
	if j.ctx.Err() != nil {
		canceled = true
	}
	for i := range j.result.Items {
		if !j.recorded[i] {
			j.result.Items[i].Status = BatchStatusCanceled
			j.result.Items[i].Err = ErrBatchCanceled
		}
	}
	j.result.WasCanceled = canceled
	j.rebuildResultListsLocked()
	j.progress.Active = 0
	j.progress.Completed = len(j.result.Items)
	finalProgress := j.progress
	j.mu.Unlock()
	j.emitProgressSnapshot(finalProgress)
}

func (j *BatchJob) runChunk(chunk batchChunk, jobs chan<- int, completions <-chan batchCompletion, active *int) bool {
	pending := chunk.indices
	inFlight := 0
	finished := 0
	stopDispatch := false

	for finished < len(chunk.indices) {
		if !stopDispatch && len(pending) == 0 {
			stopDispatch = true
		}
		if !stopDispatch && j.ctx.Err() != nil {
			stopDispatch = true
			for _, index := range pending {
				j.markCanceled(index)
				finished++
			}
			pending = nil
		}
		if stopDispatch {
			if inFlight == 0 {
				if len(pending) > 0 {
					for _, index := range pending {
						j.markCanceled(index)
						finished++
					}
					pending = nil
				}
				break
			}
			completion := <-completions
			j.recordCompletion(completion, active)
			inFlight--
			finished++
			continue
		}

		select {
		case jobs <- pending[0]:
			pending = pending[1:]
			inFlight++
			(*active)++
			j.setActive(*active)
		case completion := <-completions:
			j.recordCompletion(completion, active)
			inFlight--
			finished++
		case <-j.ctx.Done():
			stopDispatch = true
		}
	}

	return j.ctx.Err() != nil
}

func (j *BatchJob) processItem(index int) BatchItemResult {
	input := j.inputs[index]
	result := BatchItemResult{
		Index:       index,
		Source:      input.Source,
		Destination: input.Destination,
		Status:      BatchStatusFailed,
	}
	if input.Source == "" {
		result.Err = fmt.Errorf("imaging: batch item %d: empty source filename", index)
		return result
	}
	if input.Destination == "" {
		result.Err = fmt.Errorf("imaging: process %q: empty destination filename", input.Source)
		return result
	}

	src, err := Open(input.Source, j.params.DecodeOptions...)
	if err != nil {
		result.Err = fmt.Errorf("imaging: open %q: %w", input.Source, err)
		return result
	}

	dst, err := j.apply(src)
	if err != nil {
		result.Err = fmt.Errorf("imaging: process %q: %w", input.Source, err)
		return result
	}
	if dst == nil {
		result.Err = fmt.Errorf("imaging: process %q: processor returned a nil image", input.Source)
		return result
	}

	if _, err := FormatFromFilename(input.Destination); err != nil {
		result.Err = fmt.Errorf("imaging: save %q for %q: %w", input.Destination, input.Source, err)
		return result
	}
	if err := Save(dst, input.Destination, j.params.EncodeOptions...); err != nil {
		result.Err = fmt.Errorf("imaging: save %q for %q: %w", input.Destination, input.Source, err)
		return result
	}

	result.Status = BatchStatusSucceeded
	return result
}

func (j *BatchJob) apply(src image.Image) (*image.NRGBA, error) {
	if j.params.Process != nil {
		return j.params.Process(src)
	}

	switch j.params.Operation {
	case BatchResize:
		return Resize(src, j.params.Width, j.params.Height, j.params.Filter), nil
	case BatchFit:
		return Fit(src, j.params.Width, j.params.Height, j.params.Filter), nil
	case BatchFill:
		return Fill(src, j.params.Width, j.params.Height, j.params.Anchor, j.params.Filter), nil
	case BatchThumbnail:
		return Thumbnail(src, j.params.Width, j.params.Height, j.params.Filter), nil
	case BatchOriginal:
		return Clone(src), nil
	}
	return nil, fmt.Errorf("unknown operation %d", j.params.Operation)
}

func (j *BatchJob) markCanceled(index int) {
	j.mu.Lock()
	item := j.result.Items[index]
	item.Status = BatchStatusCanceled
	item.Err = ErrBatchCanceled
	j.result.Items[index] = item
	j.recorded[index] = true
	j.progress.Completed++
	j.progress.Canceled++
	progress := j.progress
	j.mu.Unlock()
	j.emitProgressSnapshot(progress)
}

func (j *BatchJob) recordCompletion(completion batchCompletion, active *int) {
	(*active)--
	j.mu.Lock()
	item := completion.result
	j.result.Items[completion.index] = item
	j.recorded[completion.index] = true
	j.progress.Completed++
	if item.Status == BatchStatusSucceeded {
		j.progress.Succeeded++
	} else if item.Status == BatchStatusFailed {
		j.progress.Failed++
	} else {
		j.progress.Canceled++
	}
	j.progress.Active = *active
	progress := j.progress
	j.mu.Unlock()
	j.emitProgressSnapshot(progress)
}

func (j *BatchJob) setActive(active int) {
	j.mu.Lock()
	j.progress.Active = active
	j.mu.Unlock()
}

func (j *BatchJob) setCurrentBatch(batch int) {
	j.mu.Lock()
	j.progress.Batch = batch
	j.mu.Unlock()
}

func (j *BatchJob) emitProgress() {
	j.mu.Lock()
	progress := j.progress
	j.mu.Unlock()
	j.emitProgressSnapshot(progress)
}

func (j *BatchJob) emitProgressSnapshot(progress BatchProgress) {
	if j.options.progress != nil {
		j.options.progress(progress)
	}
}

func (j *BatchJob) rebuildResultListsLocked() {
	j.result.Succeeded = j.result.Succeeded[:0]
	j.result.Failed = j.result.Failed[:0]
	j.result.Canceled = j.result.Canceled[:0]
	for _, item := range j.result.Items {
		switch item.Status {
		case BatchStatusSucceeded:
			j.result.Succeeded = append(j.result.Succeeded, item)
		case BatchStatusFailed:
			j.result.Failed = append(j.result.Failed, item)
		case BatchStatusCanceled:
			j.result.Canceled = append(j.result.Canceled, item)
		}
	}
}

func cloneBatchResult(result BatchResult) BatchResult {
	result.Items = append([]BatchItemResult(nil), result.Items...)
	result.Succeeded = append([]BatchItemResult(nil), result.Succeeded...)
	result.Failed = append([]BatchItemResult(nil), result.Failed...)
	result.Canceled = append([]BatchItemResult(nil), result.Canceled...)
	result.Warnings = append([]string(nil), result.Warnings...)
	return result
}
