package firstrun

import (
	"net"
	"net/url"
	"os"
	"strings"
)

// Address is where to tell someone to open Glowworm.
type Address struct {
	Base string // no trailing slash, e.g. https://money.example.com
	QR   bool   // another device could open it, so a QR code helps
	Note string // a line to go with it, e.g. why there's no QR code
}

// Where works out the address from the public URL, when one is set, or else
// from what the server listens on.
func Where(publicURL, listen string) Address {
	if publicURL != "" {
		a := Address{Base: publicURL, QR: true}
		if strings.HasPrefix(publicURL, "http://") && !isLocal(hostOf(publicURL)) {
			a.Note = "This address isn't HTTPS. Passkeys, reminders and installing Glowworm as an app need HTTPS: put it behind a proxy that adds it, such as Caddy."
		}
		return a
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return Address{Base: "http://" + listen}
	}
	switch {
	case host == "" || host == "0.0.0.0" || host == "::":
		if InContainer() {
			return Address{
				Base: "http://localhost:" + port,
				Note: "In a container, use the port it's published on. Set GLOWWORM_PUBLIC_URL to the address you open Glowworm at for a QR code.",
			}
		}
		if ip := lanIP(); ip != "" {
			return Address{
				Base: "http://" + net.JoinHostPort(ip, port), QR: true,
				Note: "From a phone or computer on the same network. Set GLOWWORM_PUBLIC_URL if you open Glowworm at another address.",
			}
		}
		return Address{Base: "http://localhost:" + port}
	case isLocal(host):
		return Address{
			Base: "http://localhost:" + port,
			Note: "Glowworm only listens on this computer. Set GLOWWORM_PUBLIC_URL to the address you open it at, through a proxy, for a QR code.",
		}
	default:
		return Address{Base: "http://" + net.JoinHostPort(host, port), QR: true}
	}
}

// SetupLink is the setup page at base, with the token when there is one.
func SetupLink(base, token string) string {
	if token == "" {
		return base + "/setup"
	}
	return base + "/setup?token=" + url.QueryEscape(token)
}

// InContainer reports whether this runs under Docker or Podman, where the
// addresses it sees aren't the ones anyone else would use.
func InContainer() bool {
	for _, f := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(f); err == nil {
			return true
		}
	}
	return false
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

func isLocal(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// lanIP is this computer's address on the local network, if it has one.
func lanIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		n, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		if ip := n.IP.To4(); ip != nil && ip.IsPrivate() {
			return ip.String()
		}
	}
	return ""
}
