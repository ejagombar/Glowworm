package static

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"sync"
)

// SplashScreen is one iPhone screen size, in CSS points, that a home screen
// app shows a launch image for. iOS only uses an apple-touch-startup-image
// whose size matches the screen exactly, so each size needs its own image.
type SplashScreen struct {
	Width, Height, Ratio int
}

// SplashScreens are the portrait iPhone screens since the iPhone 5, newest first.
var SplashScreens = []SplashScreen{
	{440, 956, 3}, // 16/17 Pro Max
	{420, 912, 3}, // Air
	{402, 874, 3}, // 16/17 Pro, 17
	{430, 932, 3}, // 14 Pro Max, 15/16 Plus, 15 Pro Max
	{393, 852, 3}, // 14 Pro, 15, 15 Pro, 16
	{428, 926, 3}, // 12/13 Pro Max, 14 Plus
	{390, 844, 3}, // 12, 13, 14, 12/13 Pro, 16e
	{375, 812, 3}, // X, XS, 11 Pro, 12/13 mini
	{414, 896, 3}, // XS Max, 11 Pro Max
	{414, 896, 2}, // XR, 11
	{414, 736, 3}, // 6/7/8 Plus
	{375, 667, 2}, // 6/7/8, SE (2nd and 3rd)
	{320, 568, 2}, // 5, SE (1st)
}

// File is the image's name under /static/splash/.
func (s SplashScreen) File(dark bool) string {
	theme := "light"
	if dark {
		theme = "dark"
	}
	return fmt.Sprintf("%dx%d-%s.png", s.Width*s.Ratio, s.Height*s.Ratio, theme)
}

// Media is the query that picks this image for a screen and colour scheme.
func (s SplashScreen) Media(dark bool) string {
	scheme := "light"
	if dark {
		scheme = "dark"
	}
	return fmt.Sprintf("(device-width: %dpx) and (device-height: %dpx) and (-webkit-device-pixel-ratio: %d) and (orientation: portrait) and (prefers-color-scheme: %s)",
		s.Width, s.Height, s.Ratio, scheme)
}

// The page background (--bg in styles.css), so the launch image gives way
// to the page without a flash.
var (
	splashLight = color.RGBA{0xee, 0xf0, 0xf3, 0xff}
	splashDark  = color.RGBA{0x0d, 0x10, 0x13, 0xff}
)

var splashCache sync.Map // file name -> []byte

// Splash is the PNG for a file name from SplashScreen.File: the glowworm in
// the middle of the page background. ok is false for any other name.
func Splash(name string) (data []byte, ok bool) {
	if v, ok := splashCache.Load(name); ok {
		return v.([]byte), true
	}
	for _, s := range SplashScreens {
		for _, dark := range []bool{false, true} {
			if s.File(dark) != name {
				continue
			}
			data, err := drawSplash(s, dark)
			if err != nil {
				return nil, false
			}
			splashCache.Store(name, data)
			return data, true
		}
	}
	return nil, false
}

func drawSplash(s SplashScreen, dark bool) ([]byte, error) {
	// The worm is drawn 128pt wide at either density.
	src, err := FS.ReadFile(fmt.Sprintf("icons/splash-worm@%dx.png", s.Ratio))
	if err != nil {
		return nil, err
	}
	worm, err := png.Decode(bytes.NewReader(src))
	if err != nil {
		return nil, err
	}
	bg := splashLight
	if dark {
		bg = splashDark
	}
	img := image.NewRGBA(image.Rect(0, 0, s.Width*s.Ratio, s.Height*s.Ratio))
	draw.Draw(img, img.Bounds(), &image.Uniform{bg}, image.Point{}, draw.Src)
	wb := worm.Bounds()
	at := image.Pt((img.Bounds().Dx()-wb.Dx())/2, (img.Bounds().Dy()-wb.Dy())/2)
	draw.Draw(img, wb.Sub(wb.Min).Add(at), worm, wb.Min, draw.Over)
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
