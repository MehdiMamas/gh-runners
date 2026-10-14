package fleet

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"
)

const codePrefix = "ghr1-"

// Code is what a join code carries.
type Code struct {
	Addrs       []string `json:"a"`
	Port        int      `json:"p"`
	Fingerprint string   `json:"f"` // SHA-256 of the host certificate
	Secret      string   `json:"s"` // one-time pairing secret
}

// Encode makes the text pasted on the new machine.
func (c Code) Encode() (string, error) {
	b, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	return codePrefix + base64.RawURLEncoding.EncodeToString(b), nil
}

var hex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ParseCode reads a join code.
func ParseCode(s string) (Code, error) {
	s = strings.TrimSpace(s)
	bad := errors.New("this is not a gh-runners join code (copy it again from Add a machine on the main host)")
	if !strings.HasPrefix(s, codePrefix) {
		return Code{}, bad
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(s, codePrefix))
	if err != nil {
		return Code{}, bad
	}
	var c Code
	if json.Unmarshal(b, &c) != nil || len(c.Addrs) == 0 || c.Port <= 0 || c.Port > 65535 || !hex64.MatchString(c.Fingerprint) || c.Secret == "" {
		return Code{}, bad
	}
	return c, nil
}

// LocalAddrs lists the addresses other machines may reach this one on:
// LAN addresses first, then Tailscale's (100.64.0.0/10) and its MagicDNS
// name when Tailscale is running.
func LocalAddrs() []string {
	var lan, ts []string
	ifs, _ := net.Interfaces()
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok || ipn.IP.IsLinkLocalUnicast() || ipn.IP.IsLoopback() {
				continue
			}
			ip := ipn.IP
			switch {
			case tailnet.Contains(ip):
				ts = append(ts, ip.String())
			case ip.To4() != nil:
				lan = append(lan, ip.String())
			case strings.HasPrefix(ip.String(), "fd7a:115c:a1e0:"):
				ts = append(ts, ip.String())
			}
		}
	}
	if name := magicDNS(); name != "" {
		ts = append(ts, name)
	}
	return append(lan, ts...)
}

var tailnet = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

// magicDNS asks the tailscale CLI for this machine's MagicDNS name.
func magicDNS() string {
	bin := "tailscale"
	if runtime.GOOS == "darwin" {
		if _, err := exec.LookPath(bin); err != nil {
			bin = "/Applications/Tailscale.app/Contents/MacOS/Tailscale"
		}
	}
	path, err := exec.LookPath(bin)
	if err != nil {
		return ""
	}
	cmd := exec.Command(path, "status", "--json")
	hideWindow(cmd)
	done := make(chan []byte, 1)
	go func() {
		out, _ := cmd.Output()
		done <- out
	}()
	var out []byte
	select {
	case out = <-done:
	case <-time.After(3 * time.Second):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return ""
	}
	var st struct {
		Self struct {
			DNSName string `json:"DNSName"`
		} `json:"Self"`
	}
	if json.Unmarshal(out, &st) != nil {
		return ""
	}
	return strings.TrimSuffix(st.Self.DNSName, ".")
}

// Commands are the one-line installs shown in Add a machine.
func Commands(code string) (windows, unix string) {
	const raw = "https://raw.githubusercontent.com/MehdiMamas/gh-runners/main/"
	windows = fmt.Sprintf("$env:GH_RUNNERS_JOIN='%s'; irm %sinstall.ps1 | iex", code, raw)
	unix = fmt.Sprintf("curl -fsSL %sinstall.sh | sh -s -- --join %s", raw, code)
	return
}
