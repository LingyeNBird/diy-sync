package client

import (
	"bufio"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"net"
	"net/url"
	"os"
	"strings"
)

// WindowsHost returns the Windows host as WSL (NAT mode) sees it: the
// default gateway, from /proc/net/route. It can change when Windows or WSL
// restarts, so clients that connected through it look it up on every start.
func WindowsHost() (string, error) {
	f, err := os.Open("/proc/net/route")
	if err != nil {
		return "", err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fl := strings.Fields(sc.Text())
		if len(fl) < 3 || fl[1] != "00000000" {
			continue
		}
		b, err := hex.DecodeString(fl[2])
		if err != nil || len(b) != 4 {
			continue
		}
		ip := make(net.IP, 4)
		binary.LittleEndian.PutUint32(ip, binary.BigEndian.Uint32(b))
		if !ip.IsUnspecified() {
			return ip.String(), nil
		}
	}
	return "", errors.New("no default route: can't find the Windows host")
}

// ServerURL is the address to use now: for a server reached through the
// Windows host address (Credentials.ViaGateway), the current one. The pinned
// certificate still decides whether it is the right server.
func (c *Credentials) ServerURL() string {
	if !c.ViaGateway {
		return c.Server
	}
	gw, err := WindowsHost()
	if err != nil {
		return c.Server
	}
	u, err := url.Parse(c.Server)
	if err != nil {
		return c.Server
	}
	port := u.Port()
	if port == "" {
		port = "7443"
	}
	u.Host = net.JoinHostPort(gw, port)
	return u.String()
}
