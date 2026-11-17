# 10-bit pipeline

## Defaults and migration

Existing calls keep their original return type, errors, and output bytes:

```go
dst := imaging.Resize(src, 800, 0, imaging.Lanczos) // unchanged, *image.NRGBA
err := imaging.Save(dst, "out.png")                  // unchanged
```

Use the depth-aware API when the source may contain more than 8 bits:

```go
src, err := imaging.Open("input.png", imaging.DecodeBitDepth(imaging.BitDepthAuto))
if err != nil {
	return err
}

dst, err := imaging.ResizeWithDepth(
	src, 800, 0, imaging.Lanczos,
	imaging.WithBitDepth(imaging.BitDepthAuto), // preserve detected depth
	imaging.WithBitDepthLogger(logDepthDecision),
)
if err != nil {
	return err
}

err = imaging.Save(dst, "out.png", imaging.EncodeBitDepth(imaging.BitDepthAuto))
```

`BitDepthAuto` is the zero value. Omitting all new options preserves the input
depth. Passing `WithBitDepth(BitDepth10)` forces 10-bit output. `DitherNone` is
the default; `DitherOrdered` is available for 8-to-10 conversion and
down-conversion.

## Depth representation

- 8-bit output remains `*image.NRGBA`.
- 10-bit and 16-bit output use `*image.NRGBA64`, with non-premultiplied RGBA
  samples.
- 10-bit samples are stored MSB-aligned in the 16-bit container. Reading
  `sample >> 6` yields the 0..1023 value.
- PNG and TIFF have only 8-bit and 16-bit storage. Their 10-bit path keeps
  exactly 10 significant bits in a 16-bit container.
- `FormatYUV420P10LE` and `.yuv` use native right-aligned 10-bit samples,
  planar Y/U/V order, full-range BT.709 conversion, and little-endian storage.

## Modules

- `depth.go`: bit-depth types, options, validation, detection, logging, 16-bit
  scanner, 10-bit quantization, and ordered dither.
- `depth_resize.go`: depth-aware resize, fit, fill, thumbnail, and mixed-depth
  batch resize.
- `depth_tools.go`: clone, crop, flips, rotations, paste, and overlay.
- `depth_filter.go`: grayscale, invert, adjustments, blur, sharpen, and
  convolution.
- `depth_io.go` / `io.go`: explicit decode/encode options, atomic save, and
  container capability errors.
- `pixel_format.go`: explicit `yuv420p10le` conversion and raw encode/decode.

## Failure behavior

- Declared depth is checked against every sample. A mismatch returns an error
  wrapping `ErrBitDepthMismatch`.
- JPEG, GIF, and BMP reject 10-bit output with an error wrapping
  `ErrUnsupportedBitDepth`; the message names PNG/TIFF or `YUV420P10LE` as
  alternatives.
- Depth-aware `Save` writes to a temporary file in the destination directory
  and renames only after a complete successful encode. On an encode, sync,
  close, or rename failure the temporary file is removed and any existing
  destination is untouched.
- A hard process kill can leave a hidden `.tmp-*` file; no userspace defer can
  run after `SIGKILL`. Normal errors and panics run cleanup.

## Logging contract

Logging is opt-in and defaults to no output. A logger receives:

- `imaging: bitdepth enter ...` at debug level with stage, input, target,
  declared depth, and dither.
- `imaging: bitdepth exit ...` at debug level with stage and output depth.
- `imaging: bitdepth fail ...` at error level with stage, input, target, and
  error.

All decisions use the same logger callback, so a batch can record which item
and stage selected each depth.

## Concurrency

The depth-aware resize/filter paths use the package's existing `parallel`
worker pool and are safe for concurrent calls. Options are per call; there is
no mutable global depth state. `Save`/`Save10` use unique temporary names in
the destination directory, so concurrent saves to different filenames are
safe. Concurrent saves to the same filename are last-rename-wins. Verify with:

```sh
go test -race ./...
```

## Size and performance

- Raw `yuv420p10le` is exactly 2x the corresponding 8-bit `yuv420p` size
  (16-bit samples for the same 4:2:0 layout).
- PNG/TIFF 10-bit output uses a 16-bit container; compressed size depends on
  content and is checked in tests to stay below 3x the 8-bit output for the
  test gradient.
- The original 8-bit `Resize` path is not reimplemented. Same-machine benchmark
  comparison before/after showed no regression beyond noise, and the
  depth-aware auto path over 8-bit input stayed within the required 5%.

## Known limitations

- Go's standard PNG and TIFF codecs do not write true 10-bit containers. The
  explicit native 10-bit output is raw `yuv420p10le`; PNG/TIFF use 16-bit
  containers with 10 significant bits.
- Automatic depth detection of a 16-bit container is a heuristic. A declaration
  via `WithDeclaredBitDepth`, `DecodeDeclaredBitDepth`, or
  `EncodeDeclaredBitDepth` should be used when the source metadata is known.
- The original 8-bit functions are intentionally not changed to auto-upgrade;
  callers must select a depth-aware function or option.
- `yuv420p10le` conversion is full-range BT.709 and does not carry color range,
  matrix, or transfer metadata.
- Arbitrary-angle rotation uses the target-depth container for its working
  copy; resizing and the common 90/180/270 transforms avoid that extra pass.
