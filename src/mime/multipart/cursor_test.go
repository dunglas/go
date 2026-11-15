// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package multipart

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"reflect"
	"testing"
)

type cursorPart struct {
	Name, FileName       string
	HasName, HasFileName bool
	ContentType, Missing string
	Body                 string
	BodyErr              bool
}

// chunkReader returns at most size bytes per read, like a network body.
type chunkReader struct {
	r    io.Reader
	size int
}

func (c chunkReader) Read(p []byte) (int, error) {
	return c.r.Read(p[:min(len(p), c.size)])
}

func cursorSource(body []byte, chunk uint8) io.Reader {
	if chunk == 0 {
		return bytes.NewReader(body)
	}
	return chunkReader{bytes.NewReader(body), int(chunk)}
}

func readPartsOld(body []byte, boundary string, skip uint64, chunk uint8) (parts []cursorPart, err error) {
	r := NewReader(cursorSource(body, chunk), boundary)
	for i := 0; ; i++ {
		p, err := r.NextRawPart()
		if err == io.EOF {
			return parts, nil
		}
		if err != nil {
			return parts, err
		}
		cp := cursorPart{Name: p.FormName(), FileName: p.FileName(), ContentType: p.Header.Get("Content-Type"), Missing: p.Header.Get("X-Missing")}
		disposition, params, err := mime.ParseMediaType(p.Header.Get("Content-Disposition"))
		if err == nil {
			_, cp.HasName = params["name"]
			cp.HasName = cp.HasName && disposition == "form-data"
			_, cp.HasFileName = params["filename"]
		}
		if skip&(1<<(i%64)) == 0 {
			b, err := io.ReadAll(p)
			cp.Body, cp.BodyErr = string(b), err != nil
		}
		parts = append(parts, cp)
	}
}

func readPartsNew(r *Reader, body []byte, boundary string, skip uint64, chunk uint8) (parts []cursorPart, err error) {
	r.Reset(cursorSource(body, chunk), boundary)
	for i := 0; ; i++ {
		if err = r.Next(); err == io.EOF {
			return parts, nil
		}
		if err != nil {
			return parts, err
		}
		var cp cursorPart
		name, ok := r.AppendFormName(nil)
		cp.Name, cp.HasName = string(name), ok
		cp.FileName, cp.HasFileName = r.FileName()
		cp.ContentType, cp.Missing = r.Header("content-type"), r.Header("X-Missing")
		if skip&(1<<(i%64)) == 0 {
			b, err := io.ReadAll(r)
			cp.Body, cp.BodyErr = string(b), err != nil
		}
		parts = append(parts, cp)
	}
}

var cursorSeeds = []string{
	"--b\r\nContent-Disposition: form-data; name=\"a\"\r\n\r\nvalue\r\n--b\r\nContent-Disposition: form-data; name=\"f\"; filename=\"dir/a.txt\"\r\nContent-Type: text/plain\r\n\r\ncontent\r\n--b--\r\n",
	"preamble\r\n--b\r\ncontent-disposition: FORM-DATA ; NAME = a ; name=a;\r\nX-A: 1\r\nx-a: 2\r\n\r\n\r\n--b--",
	"--b\nContent-Disposition: form-data; name=a\n\nlf only\n--b--\n",
	"--b\r\nContent-Disposition: form-data; name=\"caf\xc3\xa9\"; filename=\"\xc3\xa9t\xc3\xa9.txt\"\r\n\r\nx\r\n--b--\r\n",
	"--b\r\nContent-Disposition: form-data; name*=UTF-8''%C3%A9\r\n\r\nx\r\n--b--\r\n",
	"--b\r\nContent-Disposition: form-data; name=\"a\\\"b\"; filename=\"C:\\dir\\a.txt\"\r\n\r\nx\r\n--b--\r\n",
	"--b\r\nContent-Disposition: form-data;\r\n name=folded\r\n\r\nx\r\n--b--\r\n",
	"--b\r\n Content-Disposition: form-data; name=a\r\n\r\nx\r\n--b--\r\n",
	"--b\r\nContent-Disposition : form-data; name=a\r\n\r\nx\r\n--b--\r\n",
	"--b\r\nContent-Disposition: form-data; name=a; name=b\r\n\r\nx\r\n--b--\r\n",
	"--b\r\nContent-Disposition: attachment; filename=a.txt\r\n\r\nx\r\n--b--\r\n",
	"--b\r\nContent-Disposition: form-data\xc2\xa0; name=a\r\n\r\nx\r\n--b--\r\n",
	"--b\r\nContent-Disposition: form-data; name=a\r\nX-Bad\x01: 1\r\n\r\nx\r\n--b--\r\n",
	"--b\r\nContent-Disposition: form-data; name=a\r\nno colon\r\n\r\nx\r\n--b--\r\n",
	"--b\r\nContent-Disposition: form-data; name=a\r\n",
	"--b\r\nContent-Disposition: form-data; name=a\r\nbroken\r\n",
	"--b\r\nContent-Disposition: form-data; name=a\r\n\r\nbody without end",
	"--b\r\nContent-Disposition: form-data; name=a\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\nfoo=3Dbar\r\n--b--\r\n",
	"--b\r\n\r\nno headers\r\n--b--\r\n",
	"--b\r\nContent-Disposition: form-data; name=a; p1=1; p2=2; p3=3; p4=4; p5=5; p6=6; p7=7; p8=8\r\n\r\nx\r\n--b--\r\n",
	"--b\r\nA: 1\r\nB: 1\r\nC: 1\r\nD: 1\r\nE: 1\r\nF: 1\r\nG: 1\r\nH: 1\r\nI: 1\r\nJ: 1\r\nK: 1\r\nL: 1\r\nM: 1\r\nN: 1\r\nO: 1\r\nP: 1\r\nQ: 1\r\n\r\nx\r\n--b--\r\n",
}

func FuzzNext(f *testing.F) {
	for _, s := range cursorSeeds {
		f.Add([]byte(s), "b", uint64(0), uint8(0))
		f.Add([]byte(s), "b", uint64(0b1010), uint8(3))
	}
	r := new(Reader)
	f.Fuzz(func(t *testing.T, body []byte, boundary string, skip uint64, chunk uint8) {
		want, wantErr := readPartsOld(body, boundary, skip, chunk)
		got, gotErr := readPartsNew(r, body, boundary, skip, chunk)
		if !reflect.DeepEqual(got, want) || fmt.Sprint(gotErr) != fmt.Sprint(wantErr) {
			t.Errorf("body %q:\ngot  %+v, %v\nwant %+v, %v", body, got, gotErr, want, wantErr)
		}
	})
}

func cursorBody(n int) ([]byte, string) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	for i := range n {
		if err := w.WriteField(fmt.Sprintf("field%d", i), "value"); err != nil {
			panic(err)
		}
	}
	if err := w.Close(); err != nil {
		panic(err)
	}
	return buf.Bytes(), w.Boundary()
}

func TestNextAllocs(t *testing.T) {
	body, boundary := cursorBody(100)
	var src bytes.Reader
	r := new(Reader)
	var buf [64]byte
	names := make([]byte, 0, 4096)
	read := func() {
		src.Reset(body)
		r.Reset(&src, boundary)
		names = names[:0]
		for r.Next() == nil {
			var ok bool
			if names, ok = r.AppendFormName(names); !ok {
				t.Fatal("missing name")
			}
			if _, ok := r.FileName(); ok {
				t.Fatal("unexpected filename")
			}
			for {
				if _, err := r.Read(buf[:]); err != nil {
					break
				}
			}
		}
	}
	read()
	if allocs := testing.AllocsPerRun(10, read); allocs != 0 {
		t.Errorf("reading 100 parts allocated %v times, want 0", allocs)
	}
}

func BenchmarkNext(b *testing.B) {
	body, boundary := cursorBody(100)
	var buf [64]byte
	drain := func(c io.Reader) {
		for {
			if _, err := c.Read(buf[:]); err == io.EOF {
				return
			} else if err != nil {
				b.Fatal(err)
			}
		}
	}
	for _, formName := range []bool{false, true} {
		b.Run(fmt.Sprintf("FormName=%v", formName), func(b *testing.B) {
			b.Run("NextRawPart", func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(len(body)))
				for b.Loop() {
					r := NewReader(bytes.NewReader(body), boundary)
					for {
						p, err := r.NextRawPart()
						if err == io.EOF {
							break
						}
						if err != nil {
							b.Fatal(err)
						}
						if formName && p.FormName() == "" {
							b.Fatal("missing name")
						}
						drain(p)
					}
				}
			})
			b.Run("Reset+Next", func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(len(body)))
				var src bytes.Reader
				r := new(Reader)
				names := make([]byte, 0, 64)
				for b.Loop() {
					src.Reset(body)
					r.Reset(&src, boundary)
					for {
						err := r.Next()
						if err == io.EOF {
							break
						}
						if err != nil {
							b.Fatal(err)
						}
						if formName {
							var ok bool
							if names, ok = r.AppendFormName(names[:0]); !ok {
								b.Fatal("missing name")
							}
						}
						drain(r)
					}
				}
			})
		})
	}
}
