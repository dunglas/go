// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package multipart

import (
	"bytes"
	"io"
	"mime"
	"path/filepath"
)

// maxRetainedHeaderSize is the largest header buffer kept by Reset.
const maxRetainedHeaderSize = 64 << 10

// Next advances to the next part. It returns [io.EOF] after the last part.
//
// Next reads the headers of the part into a buffer that it reuses for the
// next parts, without parsing them into a map. Use [Reader.Read] to read
// the body of the part, without decoding its Content-Transfer-Encoding.
// Next reports the same errors as [Reader.NextRawPart].
func (r *Reader) Next() error {
	r.header = r.header[:0]
	r.form = formParams{}
	if err := r.nextDelimiter(); err != nil {
		return err
	}
	r.currentPart = nil
	r.cur = Part{mr: r}
	r.cur.r = partReader{&r.cur}
	var err error
	if r.header, err = appendMIMEHeader(&r.tp, r.header, maxMIMEHeaderSize, maxMIMEHeaders()); err != nil {
		return headerError(err)
	}
	r.currentPart = &r.cur
	return nil
}

// Read reads the body of the current part, after its headers and before
// the next part.
func (r *Reader) Read(p []byte) (int, error) {
	if r.currentPart != &r.cur {
		return 0, io.EOF
	}
	return r.cur.r.Read(p)
}

// AppendFormName appends to dst the name parameter of the current part if
// its Content-Disposition is of type "form-data". ok reports whether the
// part has one: if not, dst is returned unchanged.
func (r *Reader) AppendFormName(dst []byte) (_ []byte, ok bool) {
	f := r.params()
	if !f.formData || !f.hasName {
		return dst, false
	}
	return append(dst, f.name...), true
}

// FileName returns the filename parameter of the Content-Disposition of
// the current part, passed through [filepath.Base] like [Part.FileName],
// and whether the part has one.
func (r *Reader) FileName() (filename string, ok bool) {
	f := r.params()
	if !f.hasFilename {
		return "", false
	}
	if len(f.filename) == 0 {
		return "", true
	}
	// RFC 7578, Section 4.2 requires that if a filename is provided, the
	// directory path information must not be used.
	return filepath.Base(string(f.filename)), true
}

// Header returns the first value of the named header of the current part,
// like [textproto.MIMEHeader.Get].
func (r *Reader) Header(name string) string {
	v, _ := r.lookup(name)
	return string(v)
}

func (r *Reader) lookup(name string) ([]byte, bool) {
	if r.currentPart != &r.cur {
		return nil, false
	}
	for block := r.header; len(block) > 0; {
		var line []byte
		line, block, _ = bytes.Cut(block, []byte{'\n'})
		if key, value, _ := bytes.Cut(line, []byte{':'}); asciiEqualFold(key, name) {
			return value, true
		}
	}
	return nil, false
}

// formParams holds the parameters of the Content-Disposition of the current
// part, parsed once.
type formParams struct {
	parsed, formData, hasName, hasFilename bool
	name, filename                         []byte
}

func (r *Reader) params() *formParams {
	f := &r.form
	if f.parsed {
		return f
	}
	v, _ := r.lookup("Content-Disposition")
	if p, ok := parseFormData(v); ok {
		*f = p
	} else if disposition, params, err := mime.ParseMediaType(string(v)); err == nil {
		f.formData = disposition == "form-data"
		name, hasName := params["name"]
		filename, hasFilename := params["filename"]
		f.name, f.hasName = []byte(name), hasName
		f.filename, f.hasFilename = []byte(filename), hasFilename
	}
	f.parsed = r.currentPart == &r.cur
	return f
}

// parseFormData parses a "form-data" Content-Disposition like
// mime.ParseMediaType, without allocating. It reports false for the
// values mime.ParseMediaType must handle: other types, invalid values,
// RFC 2231 parameters, quoted-pairs, and non-ASCII bytes outside of quoted
// strings, which unicode.IsSpace could match.
func parseFormData(v []byte) (f formParams, ok bool) {
	base := v
	if i := bytes.IndexByte(v, ';'); i >= 0 {
		base = v[:i]
	}
	if !asciiEqualFold(trimLWSP(base), "form-data") {
		return f, false
	}
	f.formData = true

	// Duplicate parameters are allowed if their values are equal.
	var params [8]struct{ key, value []byte }
	n := 0
	rest := v[len(base):]
	for {
		if rest, ok = skipLWSP(rest); !ok {
			return f, false
		}
		if len(rest) == 0 {
			break
		}
		if rest[0] != ';' {
			return f, false
		}
		if rest, ok = skipLWSP(rest[1:]); !ok {
			return f, false
		}
		if len(rest) == 0 {
			// Ignore trailing semicolons.
			break
		}

		i := 0
		for i < len(rest) && isTokenChar(rest[i]) {
			i++
		}
		key := rest[:i]
		if len(key) == 0 || bytes.IndexByte(key, '*') >= 0 {
			return f, false
		}
		if rest, ok = skipLWSP(rest[i:]); !ok || len(rest) == 0 || rest[0] != '=' {
			return f, false
		}
		if rest, ok = skipLWSP(rest[1:]); !ok || len(rest) == 0 {
			return f, false
		}

		var value []byte
		if rest[0] == '"' {
			end := bytes.IndexByte(rest[1:], '"')
			if end < 0 {
				return f, false
			}
			value, rest = rest[1:end+1], rest[end+2:]
			for _, c := range value {
				if c == '\\' || c == '\r' || c == '\n' {
					return f, false
				}
			}
		} else {
			i = 0
			for i < len(rest) && isTokenChar(rest[i]) {
				i++
			}
			if i == 0 {
				return f, false
			}
			value, rest = rest[:i], rest[i:]
		}

		duplicate := false
		for _, p := range params[:n] {
			if bytes.EqualFold(p.key, key) {
				if !bytes.Equal(p.value, value) {
					return f, false
				}
				duplicate = true
			}
		}
		if duplicate {
			continue
		}
		if n == len(params) {
			return f, false
		}
		params[n].key, params[n].value = key, value
		n++

		switch {
		case asciiEqualFold(key, "name"):
			f.name, f.hasName = value, true
		case asciiEqualFold(key, "filename"):
			f.filename, f.hasFilename = value, true
		}
	}
	return f, true
}

// skipLWSP removes leading spaces and tabs. It reports false if
// unicode.IsSpace could match the next byte.
func skipLWSP(b []byte) ([]byte, bool) {
	b = skipLWSPChar(b)
	if len(b) > 0 && (b[0] >= 0x80 || b[0] == '\n' || b[0] == '\v' || b[0] == '\f' || b[0] == '\r') {
		return b, false
	}
	return b, true
}

func trimLWSP(b []byte) []byte {
	b = skipLWSPChar(b)
	for len(b) > 0 && (b[len(b)-1] == ' ' || b[len(b)-1] == '\t') {
		b = b[:len(b)-1]
	}
	return b
}

// asciiEqualFold reports whether b and s are equal under ASCII case folding.
func asciiEqualFold(b []byte, s string) bool {
	if len(b) != len(s) {
		return false
	}
	for i := range len(b) {
		x, y := b[i], s[i]
		if 'A' <= x && x <= 'Z' {
			x += 'a' - 'A'
		}
		if 'A' <= y && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}

// isTokenChar reports whether c is a token character of RFC 2045, like
// the mime function: a visible ASCII character other than tspecials.
func isTokenChar(c byte) bool {
	const mask = (1<<0x7f - 1<<0x21) &^ (1<<'(' | 1<<')' | 1<<'<' | 1<<'>' | 1<<'@' |
		1<<',' | 1<<';' | 1<<':' | 1<<'\\' | 1<<'"' | 1<<'/' | 1<<'[' | 1<<']' | 1<<'?' | 1<<'=')
	return ((uint64(1)<<c)&(mask&(1<<64-1)) |
		(uint64(1)<<(c-64))&(mask>>64)) != 0
}
