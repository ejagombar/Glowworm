package firstrun

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"rsc.io/qr"
)

// QR draws text as a QR code with half-block characters, two modules a
// line. With color it sets black on white itself, so it scans whatever the
// terminal's colours; without (a log file, the journal) it draws the light
// modules, which suits the usual light-on-dark terminal.
func QR(w io.Writer, text string, color bool) error {
	code, err := qr.Encode(text, qr.M)
	if err != nil {
		return err
	}
	const quiet = 2
	n := code.Size
	dark := func(x, y int) bool {
		return x >= 0 && y >= 0 && x < n && y < n && code.Black(x, y)
	}
	var b strings.Builder
	for y := -quiet; y < n+quiet; y += 2 {
		b.WriteString("  ")
		if color {
			b.WriteString("\x1b[30;107m")
		}
		for x := -quiet; x < n+quiet; x++ {
			top, bottom := dark(x, y), dark(x, y+1)
			if !color {
				top, bottom = !top, !bottom
			}
			switch {
			case top && bottom:
				b.WriteString("█")
			case top:
				b.WriteString("▀")
			case bottom:
				b.WriteString("▄")
			default:
				b.WriteString(" ")
			}
		}
		if color {
			b.WriteString("\x1b[0m")
		}
		b.WriteString("\n")
	}
	_, err = io.WriteString(w, b.String())
	return err
}

// Reachable opens base/healthz the way a visitor would and checks the answer
// comes from this server (instance, from its own /healthz), so an address
// still pointing at an old server doesn't count. Leave instance empty to
// accept any Glowworm.
func Reachable(ctx context.Context, base, instance string) error {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/healthz", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("it answered %s", resp.Status)
	}
	var h struct {
		Status   string `json:"status"`
		Instance string `json:"instance"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&h) != nil || h.Status != "ok" {
		return fmt.Errorf("something other than Glowworm answered")
	}
	if instance != "" && h.Instance != instance {
		return fmt.Errorf("a different Glowworm server answered")
	}
	return nil
}

// Banner prints the setup link, with a QR code when another device could
// open it. tried says whether Reachable ran, and reach is what it returned.
func Banner(w io.Writer, link string, a Address, tried bool, reach error, color bool) {
	var b strings.Builder
	fmt.Fprintf(&b, "\n  Glowworm is ready to set up. Open this address to create the account:\n\n    %s\n\n", link)
	if a.QR {
		_ = QR(&b, link, color)
		b.WriteString("\n")
	}
	if tried {
		if reach == nil {
			fmt.Fprintf(&b, "  Checked: %s reaches this server.\n", a.Base)
		} else {
			fmt.Fprintf(&b, "  Couldn't reach this server at %s from here (%v).\n"+
				"  Check the DNS record, the proxy and the firewall. Some routers don't let a\n"+
				"  server reach its own public address, so it may still work from outside.\n", a.Base, reach)
		}
	}
	if a.Note != "" {
		fmt.Fprintf(&b, "  %s\n", a.Note)
	}
	b.WriteString("\n")
	_, _ = io.WriteString(w, b.String())
}

// UseColor reports whether the QR code can set its own colours on f: a
// terminal, and not on Windows, whose console may print the colour codes as
// text. The plain drawing suits its usual dark background.
func UseColor(f *os.File) bool {
	if runtime.GOOS == "windows" {
		return false
	}
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}
