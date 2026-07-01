package bonpreu

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	utls "github.com/refraction-networking/utls"
)

// newChromeTransport returns an http.Transport whose TLS handshake mimics
// Chrome's JA3 fingerprint via uTLS. Bonpreu's AWS WAF scores the client's TLS
// fingerprint; a vanilla Go client's JA3 is an easy bot tell and gets a 403 /
// empty prices, so on every call we present Chrome's ClientHello.
//
// ALPN is pinned to http/1.1 so a standard http.Transport can carry the
// connection without HTTP/2 plumbing. Classic JA3 hashes the extension types,
// not the ALPN protocol list, so the fingerprint stays Chrome's.
func newChromeTransport() (*http.Transport, error) {
	if _, err := chromeSpecHTTP1(); err != nil {
		return nil, err
	}
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		return chromeDial(ctx, network, addr, chromeSpecHTTP1, func(host string) *utls.Config {
			return &utls.Config{ServerName: host}
		})
	}
	return &http.Transport{
		DialTLSContext:        dial,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: time.Second,
	}, nil
}

// chromeDial opens a TCP connection to addr and performs the uTLS Chrome
// handshake.
func chromeDial(ctx context.Context, network, addr string,
	specFn func() (utls.ClientHelloSpec, error),
	cfgFn func(host string) *utls.Config,
) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	raw, err := (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	// Fresh spec per dial — ApplyPreset may mutate it (GREASE/randomization).
	spec, err := specFn()
	if err != nil {
		raw.Close()
		return nil, err
	}
	u := utls.UClient(raw, cfgFn(host), utls.HelloCustom)
	if err := u.ApplyPreset(&spec); err != nil {
		raw.Close()
		return nil, fmt.Errorf("utls preset: %w", err)
	}
	if err := u.HandshakeContext(ctx); err != nil {
		raw.Close()
		return nil, fmt.Errorf("utls handshake: %w", err)
	}
	return u, nil
}

var chromeHelloID = utls.HelloChrome_Auto

func chromeSpecHTTP1() (utls.ClientHelloSpec, error) {
	spec, err := utls.UTLSIdToSpec(chromeHelloID)
	if err != nil {
		return spec, err
	}
	for _, ext := range spec.Extensions {
		if a, ok := ext.(*utls.ALPNExtension); ok {
			a.AlpnProtocols = []string{"http/1.1"}
		}
	}
	return spec, nil
}
