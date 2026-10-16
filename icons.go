//go:build windows

package main

import (
	"bytes"
	"embed"
	"image"
	"image/png"
	"math"
	"sync"
	"syscall"
	"unsafe"
)

// The provider logos are the official marks (lobe-icons, MIT) rasterised once
// into 96×96 alpha masks. They are tinted with the card's title colour and
// resampled to the device size at draw time, so they stay sharp at any DPI.
//
//go:embed icons/claude.png icons/codex.png icons/antigravity.png
var iconFiles embed.FS

var (
	msimg32 = syscall.NewLazyDLL("msimg32.dll")

	procAlphaBlend         = msimg32.NewProc("AlphaBlend")
	procLPtoDP             = gdi32.NewProc("LPtoDP")
	procCreateCompatibleDC = gdi32.NewProc("CreateCompatibleDC")
	procDeleteDC           = gdi32.NewProc("DeleteDC")

	iconMu    sync.Mutex
	iconMasks = map[string]*image.Gray{}
	iconSized = map[iconKey][]byte{}
)

type iconKey struct {
	id   string
	w, h int
}

// iconMask decodes (once) the alpha mask of a provider logo.
func iconMask(id string) *image.Gray {
	if mask, ok := iconMasks[id]; ok {
		return mask
	}
	var mask *image.Gray
	if data, err := iconFiles.ReadFile("icons/" + id + ".png"); err == nil {
		if decoded, err := png.Decode(bytes.NewReader(data)); err == nil {
			if gray, ok := decoded.(*image.Gray); ok {
				mask = gray
			}
		}
	}
	iconMasks[id] = mask
	return mask
}

// iconAlpha resamples the mask to w×h with 4×4 bilinear supersampling.
func iconAlpha(id string, w, h int) []byte {
	key := iconKey{id, w, h}
	if alpha, ok := iconSized[key]; ok {
		return alpha
	}
	mask := iconMask(id)
	if mask == nil {
		return nil
	}
	sw, sh := mask.Bounds().Dx(), mask.Bounds().Dy()
	sample := func(x, y float64) float64 {
		x, y = math.Min(math.Max(x-0.5, 0), float64(sw-1)), math.Min(math.Max(y-0.5, 0), float64(sh-1))
		x0, y0 := int(x), int(y)
		x1, y1 := min(x0+1, sw-1), min(y0+1, sh-1)
		fx, fy := x-float64(x0), y-float64(y0)
		at := func(px, py int) float64 { return float64(mask.Pix[py*mask.Stride+px]) }
		return (at(x0, y0)*(1-fx)+at(x1, y0)*fx)*(1-fy) + (at(x0, y1)*(1-fx)+at(x1, y1)*fx)*fy
	}
	const grid = 4
	alpha := make([]byte, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			sum := 0.0
			for j := 0; j < grid; j++ {
				for i := 0; i < grid; i++ {
					sum += sample((float64(x)+(float64(i)+0.5)/grid)*float64(sw)/float64(w),
						(float64(y)+(float64(j)+0.5)/grid)*float64(sh)/float64(h))
				}
			}
			alpha[y*w+x] = byte(math.Round(sum / (grid * grid)))
		}
	}
	iconSized[key] = alpha
	return alpha
}

// drawIcon paints the provider logo inside bounds (logical units) in color.
func drawIcon(hdc uintptr, id string, bounds rect, color uint32) {
	device := bounds
	procLPtoDP.Call(hdc, uintptr(unsafe.Pointer(&device)), 2)
	w, h := int(device.Right-device.Left), int(device.Bottom-device.Top)
	if w <= 0 || h <= 0 {
		return
	}
	iconMu.Lock()
	alpha := iconAlpha(id, w, h)
	iconMu.Unlock()
	if alpha == nil {
		return
	}

	info := bitmapInfo{Header: bitmapInfoHeader{
		Size: uint32(unsafe.Sizeof(bitmapInfoHeader{})), Width: int32(w), Height: -int32(h),
		Planes: 1, BitCount: 32, Compression: biRGB, SizeImage: uint32(w * h * 4),
	}}
	var bits unsafe.Pointer
	bitmap, _, _ := procCreateDIBSection.Call(hdc, uintptr(unsafe.Pointer(&info)), dibRGBColors, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if bitmap == 0 || bits == nil {
		return
	}
	defer procDeleteObject.Call(bitmap)
	pixels := unsafe.Slice((*byte)(bits), w*h*4)
	r, g, b := uint32(color&0xff), uint32(color>>8&0xff), uint32(color>>16&0xff)
	for i, a := range alpha {
		// AlphaBlend expects premultiplied BGRA.
		pixels[i*4+0] = byte(b * uint32(a) / 255)
		pixels[i*4+1] = byte(g * uint32(a) / 255)
		pixels[i*4+2] = byte(r * uint32(a) / 255)
		pixels[i*4+3] = a
	}

	memory, _, _ := procCreateCompatibleDC.Call(hdc)
	if memory == 0 {
		return
	}
	defer procDeleteDC.Call(memory)
	old, _, _ := procSelectObject.Call(memory, bitmap)
	defer procSelectObject.Call(memory, old)
	const blend = 255<<16 | 1<<24 // AC_SRC_OVER, SourceConstantAlpha=255, AC_SRC_ALPHA
	procAlphaBlend.Call(hdc,
		uintptr(bounds.Left), uintptr(bounds.Top), uintptr(bounds.Right-bounds.Left), uintptr(bounds.Bottom-bounds.Top),
		memory, 0, 0, uintptr(w), uintptr(h), blend)
}
