/*
Package imaging provides basic image processing functions (resize, rotate, crop, brightness/contrast adjustments, etc.).

The original processing functions accept any image type that implements
image.Image and return a new *image.NRGBA (8 bits per channel,
non-premultiplied alpha). The *WithDepth variants preserve 10-bit input or
convert to an explicitly selected depth without passing through an 8-bit
intermediate.
*/
package imaging
