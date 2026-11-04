// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package http

import (
	"bytes"
	"encoding/binary"
	"io"
	"strings"
	"syscall"
	"testing"
)

func TestWASIXEncodesARequest(t *testing.T) {
	request, err := NewRequest("POST", "https://example.com/a?b=c", strings.NewReader("hi"))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer k")
	request.Header.Set("Host", "elsewhere.example")

	got := wasixEncode(request, []byte("hi"))
	want := []byte{}
	want = binary.LittleEndian.AppendUint32(want, 4)
	want = append(want, "POST"...)
	want = binary.LittleEndian.AppendUint32(want, uint32(len("https://example.com/a?b=c")))
	want = append(want, "https://example.com/a?b=c"...)
	want = binary.LittleEndian.AppendUint32(want, 1)
	want = binary.LittleEndian.AppendUint32(want, uint32(len("Authorization")))
	want = append(want, "Authorization"...)
	want = binary.LittleEndian.AppendUint32(want, uint32(len("Bearer k")))
	want = append(want, "Bearer k"...)
	want = append(want, "hi"...)

	if !bytes.Equal(got, want) {
		t.Errorf("wasixEncode = %q, want %q", got, want)
	}
}

func TestWASIXReadsTheResponseHeadThenTheBody(t *testing.T) {
	response := []byte{}
	response = binary.LittleEndian.AppendUint32(response, 201)
	response = binary.LittleEndian.AppendUint32(response, 1)
	response = binary.LittleEndian.AppendUint32(response, uint32(len("Content-Type")))
	response = append(response, "Content-Type"...)
	response = binary.LittleEndian.AppendUint32(response, uint32(len("text/plain")))
	response = append(response, "text/plain"...)
	response = append(response, "body"...)

	transport := &wasixTransport{open: fakeHost(t, response)}
	request, err := NewRequest("GET", "https://example.com/", nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(got.Body)
	got.Body.Close()
	if err != nil {
		t.Fatal(err)
	}

	if got.StatusCode != 201 || got.Header.Get("Content-Type") != "text/plain" || string(body) != "body" {
		t.Errorf("RoundTrip = %d %v %q, want 201 text/plain body", got.StatusCode, got.Header, body)
	}
}

func TestWASIXRefusesAnotherScheme(t *testing.T) {
	transport := &wasixTransport{open: fakeHost(t, nil)}
	request, err := NewRequest("GET", "ftp://example.com/", nil)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := transport.RoundTrip(request); err == nil {
		t.Error("RoundTrip of an ftp URL succeeded")
	}
}

func TestWASIXFallsBackToTheHostsSockets(t *testing.T) {
	transport := &wasixTransport{
		open: func([]byte) (int, error) { return -1, syscall.ENOSYS },
		socket: roundTripFunc(func(req *Request) (*Response, error) {
			body, err := io.ReadAll(req.Body)
			if err != nil {
				return nil, err
			}
			return &Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
		}),
	}
	request, err := NewRequest("POST", "https://example.com/", strings.NewReader("hi"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(got.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "hi" {
		t.Errorf("the fallback read %q, want the request's body again", body)
	}
}

// fakeHost stands in for http-open: it answers every request with response.
func fakeHost(t *testing.T, response []byte) func([]byte) (int, error) {
	t.Helper()
	return func(request []byte) (int, error) {
		var fds [2]int
		if err := syscall.Pipe(fds[:]); err != nil {
			t.Fatal(err)
		}
		go func() {
			syscall.Write(fds[1], response)
			syscall.Close(fds[1])
		}()
		return fds[0], nil
	}
}
