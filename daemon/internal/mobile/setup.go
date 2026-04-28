package mobile

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

type Setup struct {
	ProxyHost    string              `json:"proxy_host"`
	ProxyPort    int                 `json:"proxy_port"`
	ProxyURL     string              `json:"proxy_url"`
	LANAddresses []string            `json:"lan_addresses"`
	QRPayloads   []string            `json:"qr_payloads"`
	CAPath       string              `json:"ca_path,omitempty"`
	Instructions map[string][]string `json:"instructions"`
}

func Build(proxyAddr, caPath string) (Setup, error) {
	host, portText, err := net.SplitHostPort(proxyAddr)
	if err != nil {
		return Setup{}, fmt.Errorf("parse proxy address: %w", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return Setup{}, fmt.Errorf("parse proxy port: %w", err)
	}
	addresses := usableLANAddresses()
	if len(addresses) == 0 || host != "" && host != "127.0.0.1" && host != "localhost" {
		if host == "" || host == "0.0.0.0" || host == "::" {
			host = "127.0.0.1"
		}
		addresses = []string{host}
	}
	proxyHost := addresses[0]
	payloads := make([]string, 0, len(addresses))
	for _, address := range addresses {
		payloads = append(payloads, fmt.Sprintf("http://%s:%d", address, port))
	}
	return Setup{
		ProxyHost:    proxyHost,
		ProxyPort:    port,
		ProxyURL:     fmt.Sprintf("http://%s:%d", proxyHost, port),
		LANAddresses: addresses,
		QRPayloads:   payloads,
		CAPath:       caPath,
		Instructions: map[string][]string{
			"ios": {
				"Set the Wi-Fi HTTP proxy to the proxy URL.",
				"Install the deconstruct root certificate profile.",
				"Enable full trust for the deconstruct certificate in Certificate Trust Settings.",
			},
			"android": {
				"Set the Wi-Fi proxy host and port.",
				"Install the deconstruct certificate as a user CA.",
				"Use an emulator or debug build that trusts user CAs for HTTPS decryption.",
			},
			"simulator": {
				"Point the simulator network proxy at the Mac proxy URL.",
				"Install and trust the deconstruct root certificate in the simulator.",
			},
		},
	}, nil
}

func usableLANAddresses() []string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var out []string
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipNet.IP
		if ip == nil || ip.IsLoopback() || ip.To4() == nil {
			continue
		}
		text := ip.String()
		if strings.HasPrefix(text, "169.254.") {
			continue
		}
		out = append(out, text)
	}
	return out
}
