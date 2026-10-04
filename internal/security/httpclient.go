package security

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"
)

func SafeHTTPClient(allowPrivate bool, timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			for _, item := range ips {
				if unsafeIP(item.IP, allowPrivate) {
					continue
				}
				return dialer.DialContext(ctx, network, net.JoinHostPort(item.IP.String(), port))
			}
			return nil, fmt.Errorf("all resolved addresses are blocked")
		},
		MaxIdleConns:        50,
		IdleConnTimeout:     60 * time.Second,
		TLSHandshakeTimeout: 5 * time.Second,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			return ValidateOutboundURL(req.URL.String(), allowPrivate)
		},
	}
}

func unsafeIP(ip net.IP, allowPrivate bool) bool {
	if ip == nil || ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}
	if !allowPrivate && ip.IsPrivate() {
		return true
	}
	_, cgnat, _ := net.ParseCIDR("100.64.0.0/10")
	_, benchmark, _ := net.ParseCIDR("198.18.0.0/15")
	if cgnat.Contains(ip) || benchmark.Contains(ip) {
		return true
	}
	return false
}
