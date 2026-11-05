package static

import (
	"bytes"
	"image/png"
	"testing"
)

func TestSplash(t *testing.T) {
	for _, s := range SplashScreens {
		for _, dark := range []bool{false, true} {
			data, ok := Splash(s.File(dark))
			if !ok {
				t.Fatalf("%s: not found", s.File(dark))
			}
			cfg, err := png.DecodeConfig(bytes.NewReader(data))
			if err != nil {
				t.Fatalf("%s: %v", s.File(dark), err)
			}
			if cfg.Width != s.Width*s.Ratio || cfg.Height != s.Height*s.Ratio {
				t.Errorf("%s: got %dx%d", s.File(dark), cfg.Width, cfg.Height)
			}
		}
	}
	if _, ok := Splash("100x100-dark.png"); ok {
		t.Error("an unlisted size was drawn")
	}
}
