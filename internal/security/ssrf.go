package security

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

func ValidateOutboundURL(raw string, allowPrivate bool) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("unsupported URL scheme")
	}
	host := u.Hostname()
	if host == "" || strings.EqualFold(host, "localhost") {
		return fmt.Errorf("invalid host")
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("resolve host: %w", err)
	}
	for _, ip := range ips {
		if ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
			return fmt.Errorf("blocked target address")
		}
		if !allowPrivate && ip.IsPrivate() {
			return fmt.Errorf("private target address is blocked")
		}
	}
	return nil
}
