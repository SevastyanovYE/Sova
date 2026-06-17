package telegrammt

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	xproxy "golang.org/x/net/proxy"
)

func proxyAwareDialContext(ctx context.Context, network string, addr string) (net.Conn, error) {
	proxyURL, err := resolveProxyURL(addr)
	if err != nil {
		return nil, err
	}
	if proxyURL == nil {
		var dialer net.Dialer
		return dialer.DialContext(ctx, network, addr)
	}
	switch strings.ToLower(proxyURL.Scheme) {
	case "http", "https":
		return dialHTTPConnectProxy(ctx, addr, proxyURL)
	case "socks5", "socks5h":
		return dialSOCKSProxy(ctx, network, addr, proxyURL)
	default:
		return nil, fmt.Errorf("unsupported proxy scheme %q", proxyURL.Scheme)
	}
}

func resolveProxyURL(target string) (*url.URL, error) {
	host, _, err := net.SplitHostPort(target)
	if err != nil {
		host = target
	}
	if shouldBypassProxy(host) {
		return nil, nil
	}
	for _, key := range []string{"ALL_PROXY", "all_proxy", "HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			parsed, err := url.Parse(value)
			if err != nil {
				return nil, fmt.Errorf("parse %s: %w", key, err)
			}
			if parsed.Scheme == "" || parsed.Host == "" {
				return nil, fmt.Errorf("%s must include scheme and host", key)
			}
			return parsed, nil
		}
	}
	return nil, nil
}

func dialHTTPConnectProxy(ctx context.Context, target string, proxyURL *url.URL) (net.Conn, error) {
	proxyAddr := proxyURL.Host
	if !strings.Contains(proxyAddr, ":") {
		if strings.EqualFold(proxyURL.Scheme, "https") {
			proxyAddr = net.JoinHostPort(proxyAddr, "443")
		} else {
			proxyAddr = net.JoinHostPort(proxyAddr, "80")
		}
	}
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", proxyAddr)
	if err != nil {
		return nil, fmt.Errorf("dial proxy %s: %w", proxyAddr, err)
	}
	success := false
	defer func() {
		if !success {
			_ = conn.Close()
		}
	}()
	if strings.EqualFold(proxyURL.Scheme, "https") {
		tlsConn := tls.Client(conn, &tls.Config{MinVersion: tls.VersionTLS12, ServerName: proxyURL.Hostname()})
		if deadline, ok := ctxDeadline(ctx); ok {
			_ = tlsConn.SetDeadline(deadline)
			defer tlsConn.SetDeadline(time.Time{})
		}
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			return nil, fmt.Errorf("https proxy handshake: %w", err)
		}
		conn = tlsConn
	}
	if deadline, ok := ctxDeadline(ctx); ok {
		_ = conn.SetDeadline(deadline)
		defer conn.SetDeadline(time.Time{})
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodConnect, "http://"+target, nil)
	if err != nil {
		return nil, fmt.Errorf("build proxy connect request: %w", err)
	}
	req.Host = target
	if user := proxyURL.User; user != nil {
		username := user.Username()
		password, _ := user.Password()
		token := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
		req.Header.Set("Proxy-Authorization", "Basic "+token)
	}
	if err := req.Write(conn); err != nil {
		return nil, fmt.Errorf("write proxy connect request: %w", err)
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, req)
	if err != nil {
		return nil, fmt.Errorf("read proxy connect response: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("proxy connect failed: %s %s", resp.Status, strings.TrimSpace(string(body)))
	}
	success = true
	return &bufferedConn{Conn: conn, reader: reader}, nil
}

func dialSOCKSProxy(ctx context.Context, network string, target string, proxyURL *url.URL) (net.Conn, error) {
	forward := &net.Dialer{}
	dialer, err := xproxy.FromURL(proxyURL, forward)
	if err != nil {
		return nil, fmt.Errorf("build socks proxy dialer: %w", err)
	}
	if contextual, ok := dialer.(interface {
		DialContext(context.Context, string, string) (net.Conn, error)
	}); ok {
		return contextual.DialContext(ctx, network, target)
	}
	type result struct {
		conn net.Conn
		err  error
	}
	done := make(chan result, 1)
	go func() {
		conn, err := dialer.Dial(network, target)
		done <- result{conn: conn, err: err}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-done:
		return result.conn, result.err
	}
}

func shouldBypassProxy(host string) bool {
	host = strings.TrimSpace(strings.ToLower(host))
	if host == "" {
		return false
	}
	hostIP := net.ParseIP(host)
	for _, raw := range strings.Split(firstProxyEnv("NO_PROXY", "no_proxy"), ",") {
		entry := strings.TrimSpace(strings.ToLower(raw))
		if entry == "" {
			continue
		}
		if entry == "*" {
			return true
		}
		if _, cidr, err := net.ParseCIDR(entry); err == nil && hostIP != nil && cidr.Contains(hostIP) {
			return true
		}
		if ip := net.ParseIP(entry); ip != nil && hostIP != nil && ip.Equal(hostIP) {
			return true
		}
		if strings.HasPrefix(entry, ".") {
			trimmed := strings.TrimPrefix(entry, ".")
			if host == trimmed || strings.HasSuffix(host, entry) {
				return true
			}
			continue
		}
		if host == entry || strings.HasSuffix(host, "."+entry) {
			return true
		}
	}
	return false
}

func firstProxyEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

func ctxDeadline(ctx context.Context) (time.Time, bool) {
	if ctx == nil {
		return time.Time{}, false
	}
	return ctx.Deadline()
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) {
	return c.reader.Read(p)
}
