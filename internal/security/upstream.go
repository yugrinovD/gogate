package security

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

// ValidateUpstreamURLSyntax performs offline URL validation. It intentionally
// does not resolve DNS, so control-plane reloads do not depend on the resolver.
func ValidateUpstreamURLSyntax(raw string) error {
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
	if u.Hostname() == "" {
		return fmt.Errorf("upstream url has no host")
	}
	return nil
}

// ValidateUpstreamURLStatic applies offline policy: syntax, metadata hostnames,
// and IP-literal private/loopback checks. Hostnames are not resolved to keep
// control-plane reloads independent of DNS.
func ValidateUpstreamURLStatic(raw string, allowPrivate bool) error {
	if err := ValidateUpstreamURLSyntax(raw); err != nil {
		return err
	}
	u, _ := url.Parse(raw)
	host := u.Hostname()
	if strings.EqualFold(host, "metadata.google.internal") || strings.EqualFold(host, "metadata") {
		return fmt.Errorf("metadata host is not allowed")
	}
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
		if !allowPrivate {
			return fmt.Errorf("private upstream address %s is not allowed", host)
		}
		return nil
	}
	if ip := net.ParseIP(host); ip != nil {
		return validateIP(ip, allowPrivate)
	}
	return nil
}

// ValidateUpstreamHost resolves a host and rejects metadata, link-local,
// multicast and (unless explicitly allowed) private/loopback addresses.
func ValidateUpstreamHost(raw string, allowPrivate bool) error {
	if err := ValidateUpstreamURLSyntax(raw); err != nil {
		return err
	}
	u, _ := url.Parse(raw)
	host := u.Hostname()
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

// ValidateUpstreamURL is the full validation used when administrators create or
// update a provider.
func ValidateUpstreamURL(raw string, allowPrivate bool) error {
	return ValidateUpstreamHost(raw, allowPrivate)
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
