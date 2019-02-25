// Copyright 2012 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package http

import (
	"bufio"
	"bytes"
	"io"
	"io/ioutil"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestBodyReadBadTrailer(t *testing.T) {
	b := &body{
		src: strings.NewReader("foobar"),
		hdr: true, // force reading the trailer
		r:   bufio.NewReader(strings.NewReader("")),
	}
	buf := make([]byte, 7)
	n, err := b.Read(buf[:3])
	got := string(buf[:n])
	if got != "foo" || err != nil {
		t.Fatalf(`first Read = %d (%q), %v; want 3 ("foo")`, n, got, err)
	}

	n, err = b.Read(buf[:])
	got = string(buf[:n])
	if got != "bar" || err != nil {
		t.Fatalf(`second Read = %d (%q), %v; want 3 ("bar")`, n, got, err)
	}

	n, err = b.Read(buf[:])
	got = string(buf[:n])
	if err == nil {
		t.Errorf("final Read was successful (%q), expected error from trailer read", got)
	}
}

func TestFinalChunkedBodyReadEOF(t *testing.T) {
	res, err := ReadResponse(bufio.NewReader(strings.NewReader(
		"HTTP/1.1 200 OK\r\n"+
			"Transfer-Encoding: chunked\r\n"+
			"\r\n"+
			"0a\r\n"+
			"Body here\n\r\n"+
			"09\r\n"+
			"continued\r\n"+
			"0\r\n"+
			"\r\n")), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := "Body here\ncontinued"
	buf := make([]byte, len(want))
	n, err := res.Body.Read(buf)
	if n != len(want) || err != io.EOF {
		t.Logf("body = %#v", res.Body)
		t.Errorf("Read = %v, %v; want %d, EOF", n, err, len(want))
	}
	if string(buf) != want {
		t.Errorf("buf = %q; want %q", buf, want)
	}
}

func TestDetectInMemoryReaders(t *testing.T) {
	pr, _ := io.Pipe()
	tests := []struct {
		r    io.Reader
		want bool
	}{
		{pr, false},

		{bytes.NewReader(nil), true},
		{bytes.NewBuffer(nil), true},
		{strings.NewReader(""), true},

		{ioutil.NopCloser(pr), false},

		{ioutil.NopCloser(bytes.NewReader(nil)), true},
		{ioutil.NopCloser(bytes.NewBuffer(nil)), true},
		{ioutil.NopCloser(strings.NewReader("")), true},
	}
	for i, tt := range tests {
		got := isKnownInMemoryReader(tt.r)
		if got != tt.want {
			t.Errorf("%d: got = %v; want %v", i, got, tt.want)
		}
	}
}

type mockTransferWriterBodyWriter struct {
	CalledReader io.Reader
	WriteCalled  bool
}

func (w *mockTransferWriterBodyWriter) ReadFrom(r io.Reader) (int64, error) {
	w.CalledReader = r
	return io.Copy(ioutil.Discard, r)
}

func (w *mockTransferWriterBodyWriter) Write(p []byte) (int, error) {
	w.WriteCalled = true
	return ioutil.Discard.Write(p)
}

func TestTransferWriterWriteBodyReaderTypes(t *testing.T) {
	fileTyp := reflect.TypeOf(&os.File{})
	bufferTyp := reflect.TypeOf(&bytes.Buffer{})

	newFileFunc := func() (io.Reader, func(), error) {
		f, err := ioutil.TempFile("", "net-http-testtransferwriterwritebodyreadertypes")
		if err != nil {
			return nil, nil, err
		}

		// 1K zeros just to get a file that we can read
		_, err = f.Write(make([]byte, 1024))
		f.Close()
		if err != nil {
			return nil, nil, err
		}

		f, err = os.Open(f.Name())
		if err != nil {
			return nil, nil, err
		}

		return f, func() {
			f.Close()
			os.Remove(f.Name())
		}, nil
	}

	newBufferFunc := func() (io.Reader, func(), error) {
		return bytes.NewBuffer(make([]byte, 1024)), func() {}, nil
	}

	cases := []struct {
		Name             string
		BodyFunc         func() (io.Reader, func(), error)
		Method           string
		ContentLength    int64
		TransferEncoding []string
		LimitedReader    bool
		ExpectedReader   reflect.Type
		ExpectedWrite    bool
	}{
		{
			Name:           "file, non-chunked, size set",
			BodyFunc:       newFileFunc,
			Method:         "PUT",
			ContentLength:  1024,
			LimitedReader:  true,
			ExpectedReader: fileTyp,
		},
		{
			Name:   "file, non-chunked, size set, nopCloser wrapped",
			Method: "PUT",
			BodyFunc: func() (io.Reader, func(), error) {
				r, cleanup, err := newFileFunc()
				return ioutil.NopCloser(r), cleanup, err
			},
			ContentLength:  1024,
			LimitedReader:  true,
			ExpectedReader: fileTyp,
		},
		{
			Name:           "file, non-chunked, negative size",
			Method:         "PUT",
			BodyFunc:       newFileFunc,
			ContentLength:  -1,
			ExpectedReader: fileTyp,
		},
		{
			Name:           "file, non-chunked, CONNECT, negative size",
			Method:         "CONNECT",
			BodyFunc:       newFileFunc,
			ContentLength:  -1,
			ExpectedReader: fileTyp,
		},
		{
			Name:             "file, chunked",
			Method:           "PUT",
			BodyFunc:         newFileFunc,
			TransferEncoding: []string{"chunked"},
			ExpectedWrite:    true,
		},
		{
			Name:           "buffer, non-chunked, size set",
			BodyFunc:       newBufferFunc,
			Method:         "PUT",
			ContentLength:  1024,
			LimitedReader:  true,
			ExpectedReader: bufferTyp,
		},
		{
			Name:   "buffer, non-chunked, size set, nopCloser wrapped",
			Method: "PUT",
			BodyFunc: func() (io.Reader, func(), error) {
				r, cleanup, err := newBufferFunc()
				return ioutil.NopCloser(r), cleanup, err
			},
			ContentLength:  1024,
			LimitedReader:  true,
			ExpectedReader: bufferTyp,
		},
		{
			Name:          "buffer, non-chunked, negative size",
			Method:        "PUT",
			BodyFunc:      newBufferFunc,
			ContentLength: -1,
			ExpectedWrite: true,
		},
		{
			Name:          "buffer, non-chunked, CONNECT, negative size",
			Method:        "CONNECT",
			BodyFunc:      newBufferFunc,
			ContentLength: -1,
			ExpectedWrite: true,
		},
		{
			Name:             "buffer, chunked",
			Method:           "PUT",
			BodyFunc:         newBufferFunc,
			TransferEncoding: []string{"chunked"},
			ExpectedWrite:    true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			body, cleanup, err := tc.BodyFunc()
			if err != nil {
				t.Fatal(err)
			}

			defer cleanup()

			mw := &mockTransferWriterBodyWriter{}
			tw := &transferWriter{
				Body:             body,
				ContentLength:    tc.ContentLength,
				TransferEncoding: tc.TransferEncoding,
			}

			if err := tw.writeBody(mw); err != nil {
				t.Fatal(err)
			}

			if tc.ExpectedReader != nil {
				if mw.CalledReader == nil {
					t.Fatal("expected ReadFrom to be called, but it wasn't")
				}

				var actualReader reflect.Type
				lr, ok := mw.CalledReader.(*io.LimitedReader)
				if ok && tc.LimitedReader {
					actualReader = reflect.TypeOf(lr.R)
				} else {
					actualReader = reflect.TypeOf(mw.CalledReader)
				}

				if tc.ExpectedReader != actualReader {
					t.Fatalf("expected reader to be %s, got %s", tc.ExpectedReader, actualReader)
				}
			}

			if tc.ExpectedWrite && !mw.WriteCalled {
				t.Fatal("expected Read to be called, but it wasn't")
			}
		})
	}
}
