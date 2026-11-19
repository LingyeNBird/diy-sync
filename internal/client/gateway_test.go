package client

import (
	"strings"
	"testing"
)

func TestServerURLFollowsGateway(t *testing.T) {
	gw, err := WindowsHost()
	if err != nil {
		t.Skip("no default route here:", err)
	}
	c := &Credentials{Server: "https://10.255.255.1:7443"}
	if c.ServerURL() != c.Server {
		t.Fatal("a fixed address must stay")
	}
	c.ViaGateway = true
	if got := c.ServerURL(); got != "https://"+gw+":7443" || strings.Contains(got, "10.255") {
		t.Fatalf("ServerURL = %s, gateway %s", got, gw)
	}
}
