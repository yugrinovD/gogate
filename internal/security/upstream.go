package security

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

// ValidateUpstreamURL rejects unsafe provider URLs. Only administrators can set
// these URLs, but validating them defends against misconfiguration and SSRF.
func ValidateUpstreamURL(raw string, allowPrivate bool) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("upstream url is empty")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse upstream url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("upstream url scheme must be http or https")
	}
	if u.User != nil {
		return fmt.Errorf("upstream url must not contain credentials")
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("upstream url has no host")
	}
	if strings.EqualFold(host, "metadata.google.internal") || strings.EqualFold(host, "metadata") {
		return fmt.Errorf("metadata host is not allowed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return fmt.Errorf("resolve upstream host: %w", err)
	}
	for _, ip := range ips {
		if err := validateIP(ip.IP, allowPrivate); err != nil {
			return err
		}
	}
	if len(ips) == 0 {
		return fmt.Errorf("upstream host did not resolve")
	}
	return nil
}

func validateIP(ip net.IP, allowPrivate bool) error {
	if ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() {
		return fmt.Errorf("upstream address %s is not allowed", ip)
	}
	if ip.Equal(net.ParseIP("169.254.169.254")) {
		return fmt.Errorf("cloud metadata address is not allowed")
	}
	if !allowPrivate && (ip.IsLoopback() || ip.IsPrivate()) {
		return fmt.Errorf("private upstream address %s is not allowed", ip)
	}
	return nil
}
