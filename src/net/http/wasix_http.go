// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package http

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// A transport over wasixv2's http-open, which hands a whole request to the host as data: the
// host's own HTTP client carries it, TLS included, so a guest that has no TLS and no
// transparent socket still reaches https. The request and the response are one encoding of
// little-endian u32 counts and then the bytes (wasixv2-abi's wit/ext.wit).

// wasixRefused are the headers the host sends itself, or that describe a connection a host's
// HTTP client does not have.
var wasixRefused = map[string]bool{
	"host":            true,
	"content-length":  true,
	"connection":      true,
	"accept-encoding": true,
	"user-agent":      true,
}

// wasixMaxHeaders bounds a response head a host could otherwise make us allocate for.
const wasixMaxHeaders = 1 << 12

// wasixTransport is a RoundTripper over the host's HTTP client.
type wasixTransport struct {
	// open asks the host to carry one encoded request and returns the descriptor its response is
	// read from.
	open func(request []byte) (fd int, err error)
	// socket carries a request the host cannot: a runtime without http-open answers ENOSYS.
	socket RoundTripper
}

func (t *wasixTransport) RoundTrip(req *Request) (*Response, error) {
	if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
		return nil, fmt.Errorf("wasix http-open: %s: unsupported protocol scheme", req.URL.Scheme)
	}
	body, err := wasixBody(req)
	if err != nil {
		return nil, err
	}
	fd, err := t.open(wasixEncode(req, body))
	if err != nil {
		if t.socket != nil && errors.Is(err, syscall.ENOSYS) {
			return t.socket.RoundTrip(wasixReplay(req, body))
		}
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "http-open")
	reader := bufio.NewReaderSize(file, 4096)
	status, header, err := wasixHead(reader)
	if err != nil {
		file.Close()
		return nil, err
	}
	length := int64(-1)
	if value := header.Get("Content-Length"); value != "" {
		if n, err := strconv.ParseInt(value, 10, 64); err == nil {
			length = n
		}
	}
	return &Response{
		Status:        fmt.Sprintf("%d %s", status, StatusText(status)),
		StatusCode:    status,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        header,
		Body:          &wasixResponseBody{Reader: reader, file: file},
		ContentLength: length,
		Close:         true,
		Request:       req,
	}, nil
}

// wasixResponseBody is the rest of the host's descriptor: what the head's read buffered, then
// what has not arrived yet.
type wasixResponseBody struct {
	*bufio.Reader
	file *os.File
}

func (b *wasixResponseBody) Close() error {
	return b.file.Close()
}

// wasixBody reads the request's body, which the encoding carries whole.
func wasixBody(req *Request) ([]byte, error) {
	if req.Body == nil {
		return nil, nil
	}
	defer req.Body.Close()
	return io.ReadAll(req.Body)
}

// wasixReplay is req again with the body wasixBody read, for the transport that sends it itself.
func wasixReplay(req *Request, body []byte) *Request {
	clone := new(Request)
	*clone = *req
	if body == nil {
		clone.Body = NoBody
		clone.GetBody = func() (io.ReadCloser, error) { return NoBody, nil }
	} else {
		replay := func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
		clone.Body, _ = replay()
		clone.GetBody = replay
	}
	return clone
}

// wasixEncode is one request in the ABI's encoding: the method, the URL, the headers, the body.
func wasixEncode(req *Request, body []byte) []byte {
	out := appendWASIXString(nil, req.Method)
	out = appendWASIXString(out, req.URL.String())
	headers := make([][2]string, 0, len(req.Header)+1)
	for name, values := range req.Header {
		if wasixRefused[strings.ToLower(name)] || len(values) == 0 {
			continue
		}
		headers = append(headers, [2]string{name, strings.Join(values, ", ")})
	}
	out = binary.LittleEndian.AppendUint32(out, uint32(len(headers)))
	for _, header := range headers {
		out = appendWASIXString(out, header[0])
		out = appendWASIXString(out, header[1])
	}
	return append(out, body...)
}

// wasixHead reads a response's status and headers; its body follows them.
func wasixHead(r *bufio.Reader) (int, Header, error) {
	status, err := wasixUint32(r)
	if err != nil {
		return 0, nil, err
	}
	count, err := wasixUint32(r)
	if err != nil {
		return 0, nil, err
	}
	if count > wasixMaxHeaders {
		return 0, nil, errors.New("wasix http-open: the response has too many headers")
	}
	header := make(Header, count)
	for range count {
		name, err := wasixString(r)
		if err != nil {
			return 0, nil, err
		}
		value, err := wasixString(r)
		if err != nil {
			return 0, nil, err
		}
		header.Add(name, value)
	}
	return int(status), header, nil
}

func appendWASIXString(out []byte, s string) []byte {
	out = binary.LittleEndian.AppendUint32(out, uint32(len(s)))
	return append(out, s...)
}

func wasixUint32(r *bufio.Reader) (uint32, error) {
	var word [4]byte
	if _, err := io.ReadFull(r, word[:]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(word[:]), nil
}

func wasixString(r *bufio.Reader) (string, error) {
	length, err := wasixUint32(r)
	if err != nil {
		return "", err
	}
	bytes := make([]byte, length)
	if _, err := io.ReadFull(r, bytes); err != nil {
		return "", err
	}
	return string(bytes), nil
}
