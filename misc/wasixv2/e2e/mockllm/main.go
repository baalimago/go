// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Mockllm stands in for an LLM provider in the wasixv2 end-to-end test.
//
// It is an HTTPS proxy: a client that sets HTTPS_PROXY to it gets every
// CONNECT tunnel answered by mockllm itself, with TLS terminated by a
// certificate from a throwaway CA, so the guest exercises the same CONNECT,
// TLS and server-sent-events path as against a real provider. Any
// .../chat/completions request is answered with a fixed reply, streamed one
// word at a time.
//
// Usage:
//
//	mockllm -ca ca.pem -addr-file addr
//
// mockllm writes the CA certificate to -ca, listens on -addr and, once it
// accepts connections, writes the address it listens on to -addr-file.
package main

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// Reply is what every completion streams, one element per chunk.
var Reply = []string{"Hello", " from", " Go", " on", " wasixv2", "!"}

const chunkDelay = 50 * time.Millisecond

func main() {
	addr := flag.String("addr", "127.0.0.1:0", "listen address")
	caFile := flag.String("ca", "ca.pem", "file to write the CA certificate to")
	addrFile := flag.String("addr-file", "", "file to write the listen address to")
	flag.Parse()
	log.SetPrefix("mockllm: ")
	log.SetFlags(0)
	if err := run(*addr, *caFile, *addrFile); err != nil {
		log.Fatal(err)
	}
}

func run(addr, caFile, addrFile string) error {
	ca, err := newCA()
	if err != nil {
		return err
	}
	if err := os.WriteFile(caFile, ca.pem, 0o644); err != nil {
		return err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	if addrFile != "" {
		if err := os.WriteFile(addrFile, []byte(ln.Addr().String()), 0o644); err != nil {
			return err
		}
	}
	log.Printf("listening on %s", ln.Addr())
	return http.Serve(ln, &proxy{ca: ca})
}

type ca struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func newCA() (*ca, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "mockllm test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &ca{cert, key, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}, nil
}

func (c *ca) leaf(host string) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: host},
		DNSNames:     []string{host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, &key.PublicKey, c.key)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}

type proxy struct{ ca *ca }

func (p *proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodConnect {
		http.Error(w, "mockllm only serves CONNECT", http.StatusMethodNotAllowed)
		return
	}
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	cert, err := p.ca.leaf(host)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	conn, rw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		log.Printf("CONNECT %s: %v", r.Host, err)
		return
	}
	defer conn.Close()
	if _, err := rw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	if err := rw.Flush(); err != nil {
		return
	}
	tc := tls.Server(conn, &tls.Config{
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{"http/1.1"},
	})
	if err := tc.Handshake(); err != nil {
		log.Printf("CONNECT %s: TLS handshake: %v", r.Host, err)
		return
	}
	state := tc.ConnectionState()
	log.Printf("CONNECT %s: %s, %s", r.Host, tls.VersionName(state.Version), tls.CipherSuiteName(state.CipherSuite))
	if err := serveTunnel(tc); err != nil && !errors.Is(err, io.EOF) {
		log.Printf("CONNECT %s: %v", r.Host, err)
	}
}

// serveTunnel answers HTTP/1.1 requests on c until the client closes it.
func serveTunnel(c net.Conn) error {
	br := bufio.NewReader(c)
	for {
		req, err := http.ReadRequest(br)
		if err != nil {
			return err
		}
		if _, err := io.Copy(io.Discard, req.Body); err != nil {
			return err
		}
		log.Printf("%s https://%s%s", req.Method, req.Host, req.URL.Path)
		if err := respond(c, req); err != nil {
			return err
		}
		if req.Close {
			return nil
		}
	}
}

func respond(w io.Writer, req *http.Request) error {
	if req.Method != http.MethodPost || !strings.HasSuffix(req.URL.Path, "/chat/completions") {
		const body = `{"error":"mockllm only serves POST .../chat/completions"}`
		_, err := fmt.Fprintf(w, "HTTP/1.1 404 Not Found\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", len(body), body)
		return err
	}
	if _, err := io.WriteString(w, "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nTransfer-Encoding: chunked\r\n\r\n"); err != nil {
		return err
	}
	for _, word := range Reply {
		if err := event(w, map[string]any{"content": word}, nil); err != nil {
			return err
		}
		time.Sleep(chunkDelay)
	}
	stop := "stop"
	if err := event(w, map[string]any{}, &stop); err != nil {
		return err
	}
	if err := chunk(w, "data: [DONE]\n\n"); err != nil {
		return err
	}
	return chunk(w, "")
}

// event writes one OpenAI chat.completion.chunk event.
func event(w io.Writer, delta map[string]any, finish *string) error {
	data, err := json.Marshal(map[string]any{
		"id":      "mockllm",
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   "mockllm",
		"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}},
	})
	if err != nil {
		return err
	}
	return chunk(w, "data: "+string(data)+"\n\n")
}

// chunk writes s as one HTTP/1.1 chunk; the empty string ends the body.
func chunk(w io.Writer, s string) error {
	_, err := fmt.Fprintf(w, "%x\r\n%s\r\n", len(s), s)
	return err
}
