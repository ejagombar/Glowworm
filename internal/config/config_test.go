package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPublicURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://money.example.com":       "https://money.example.com",
		"https://money.example.com/":      "https://money.example.com",
		"http://192.168.1.4:8080":         "http://192.168.1.4:8080",
		"money.example.com":               "",
		"ftp://money.example.com":         "",
		"https://money.example.com/app":   "",
		"https://money.example.com/?x=1":  "",
		"https://user:pw@money.example.c": "",
	} {
		t.Setenv("GLOWWORM_PUBLIC_URL", in)
		c, err := Load()
		if want == "" {
			if err == nil {
				t.Errorf("%q accepted as %q", in, c.PublicURL)
			}
			continue
		}
		if err != nil || c.PublicURL != want {
			t.Errorf("%q: %q, %v", in, c.PublicURL, err)
		}
	}
}

func TestPublicURLFromFile(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "glowworm.toml"), []byte("public_url = \"https://m.example/\"\nsetup_token = \"off\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load()
	if err != nil || c.PublicURL != "https://m.example" || c.SetupToken != "off" {
		t.Errorf("%+v %v", c, err)
	}
}

func TestUpdateCheck(t *testing.T) {
	for in, want := range map[string]string{"": "on", "on": "on", "OFF": "off", "false": "off"} {
		t.Setenv("GLOWWORM_UPDATE_CHECK", in)
		if c, err := Load(); err != nil || c.UpdateCheck != want {
			t.Errorf("%q: %q %v", in, c.UpdateCheck, err)
		}
	}
	t.Setenv("GLOWWORM_UPDATE_CHECK", "weekly")
	if _, err := Load(); err == nil {
		t.Error("weekly accepted")
	}
}
