// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package multipart

import (
	"bufio"
	"bytes"
	"io"
	"math"
	"mime"
	"mime/quotedprintable"
	"net/textproto"
	"path/filepath"
	"strings"
)

// maxRetainedHeaderSize is the largest header buffer kept by Reset.
const maxRetainedHeaderSize = 64 << 10

// maxSimpleHeaderKeys is the number of distinct header keys of a part
// that Next can read without net/textproto.
const maxSimpleHeaderKeys = 16

// Next advances to the next part. It returns [io.EOF] after the last part.
//
// Next reads the headers of the part into a buffer that it reuses for the
// next parts, without parsing them into a map. The methods of [Reader]
// returning information about the current part read this buffer.
// Next reports the same errors as [Reader.NextRawPart].
func (r *Reader) Next() error {
	r.slowHeader = nil
	r.header = r.header[:0]
	r.disposition, r.hasDisposition = nil, false
	r.decoded = false
	if r.currentPart == &r.cur {
		// Skip the rest of the body without decoding it.
		r.cur.r = partReader{&r.cur}
	}
	if err := r.nextDelimiter(); err != nil {
		return err
	}
	r.currentPart = nil
	r.cur = Part{mr: r}
	r.cur.r = partReader{&r.cur}
	if err := r.readHeader(maxMIMEHeaderSize, maxMIMEHeaders()); err != nil {
		return err
	}
	r.currentPart = &r.cur
	return nil
}

// FormName returns the name parameter of the current part if its
// Content-Disposition is of type "form-data", like [Part.FormName].
func (r *Reader) FormName() string {
	if r.currentPart != &r.cur {
		return ""
	}
	if r.slowHeader == nil {
		v, _ := r.lookup("Content-Disposition")
		if formData, name, _, ok := parseFormData(v); ok {
			if !formData {
				return ""
			}
			return string(name)
		}
	}
	disposition, params, err := mime.ParseMediaType(r.Header("Content-Disposition"))
	if err != nil || disposition != "form-data" {
		return ""
	}
	return params["name"]
}

// FileName returns the filename parameter of the Content-Disposition of
// the current part, like [Part.FileName].
func (r *Reader) FileName() string {
	if r.currentPart != &r.cur {
		return ""
	}
	var filename string
	if v, _ := r.lookup("Content-Disposition"); r.slowHeader == nil {
		if formData, _, f, ok := parseFormData(v); ok && formData {
			if len(f) == 0 {
				return ""
			}
			filename = string(f)
		}
	}
	if filename == "" {
		_, params, err := mime.ParseMediaType(r.Header("Content-Disposition"))
		if err != nil || params["filename"] == "" {
			return ""
		}
		filename = params["filename"]
	}
	// RFC 7578, Section 4.2 requires that if a filename is provided, the
	// directory path information must not be used.
	return filepath.Base(filename)
}

// Header returns the first value of the named header of the current part,
// like [textproto.MIMEHeader.Get]. It doesn't parse the other headers.
func (r *Reader) Header(name string) string {
	if r.currentPart != &r.cur {
		return ""
	}
	if r.slowHeader != nil {
		return r.slowHeader.Get(name)
	}
	v, _ := r.lookup(name)
	return string(v)
}

// Headers returns all the headers of the current part.
func (r *Reader) Headers() textproto.MIMEHeader {
	if r.currentPart != &r.cur {
		return nil
	}
	h, _ := readMIMEHeader(textproto.NewReader(bufio.NewReader(bytes.NewReader(r.header))), math.MaxInt64, math.MaxInt64)
	return h
}

// Content returns a reader of the body of the current part. As a special
// case, if the "Content-Transfer-Encoding" header has a value of
// "quoted-printable", the body is decoded, like with [Reader.NextPart].
func (r *Reader) Content() io.Reader {
	if r.currentPart != &r.cur {
		return eofReader{}
	}
	if !r.decoded {
		r.decoded = true
		if strings.EqualFold(r.Header("Content-Transfer-Encoding"), "quoted-printable") {
			r.cur.r = quotedprintable.NewReader(partReader{&r.cur})
		}
	}
	return contentReader{&r.cur}
}

// RawContent returns a reader of the body of the current part, without
// decoding the Content-Transfer-Encoding. It can't be mixed with
// [Reader.Content].
func (r *Reader) RawContent() io.Reader {
	if r.currentPart != &r.cur {
		return eofReader{}
	}
	return partReader{&r.cur}
}

type contentReader struct{ p *Part }

func (c contentReader) Read(b []byte) (int, error) { return c.p.r.Read(b) }

type eofReader struct{}

func (eofReader) Read([]byte) (int, error) { return 0, io.EOF }

// readHeader reads the headers of the current part into r.header. When
// they aren't simple, it parses them with net/textproto, which reports
// the same errors as NextPart.
func (r *Reader) readHeader(maxMemory, maxHeaders int64) error {
	block := r.header[:0]
	lineStart := 0
	for {
		chunk, err := r.bufReader.ReadSlice('\n')
		block = append(block, chunk...)
		r.header = block
		if int64(len(block)) > maxMemory {
			return ErrMessageTooLarge
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil {
			return r.parseSlowHeader(maxMemory, maxHeaders, err)
		}
		if len(trimEOL(block[lineStart:])) == 0 {
			break
		}
		lineStart = len(block)
	}
	if !r.simpleHeader(block, maxMemory, maxHeaders) {
		return r.parseSlowHeader(maxMemory, maxHeaders, nil)
	}
	return nil
}

// parseSlowHeader parses r.header like NextPart. readErr is the error
// that ended the headers before an empty line.
func (r *Reader) parseSlowHeader(maxMemory, maxHeaders int64, readErr error) error {
	header, err := readMIMEHeader(textproto.NewReader(bufio.NewReader(bytes.NewReader(r.header))), maxMemory, maxHeaders)
	if err == io.EOF && readErr != nil {
		err = readErr
	}
	if err != nil {
		// TODO: Add a distinguishable error to net/textproto.
		if err.Error() == "message too large" {
			err = ErrMessageTooLarge
		}
		return err
	}
	r.slowHeader = header
	return nil
}

// simpleHeader reports whether the headers in block can be read without
// net/textproto: lines without continuation, which net/textproto would
// accept within the limits.
func (r *Reader) simpleHeader(block []byte, maxMemory, maxHeaders int64) bool {
	r.disposition, r.hasDisposition = nil, false
	var keys [maxSimpleHeaderKeys][]byte
	n := 0
	for {
		i := bytes.IndexByte(block, '\n')
		line := trimEOL(block[:i+1])
		block = block[i+1:]
		if len(line) == 0 {
			return true
		}
		if int64(len(line)) > maxMemory {
			return false
		}
		key, value, ok := splitHeaderLine(line)
		if !ok {
			return false
		}
		if maxHeaders--; maxHeaders < 0 {
			return false
		}
		seen := false
		for _, k := range keys[:n] {
			if bytes.EqualFold(k, key) {
				seen = true
				break
			}
		}
		if !r.hasDisposition && asciiEqualFold(key, "Content-Disposition") {
			r.disposition, r.hasDisposition = value, true
		}
		if !seen {
			if n == len(keys) {
				return false
			}
			keys[n] = key
			n++
			const mapEntryOverhead = 200
			maxMemory -= int64(len(key)) + mapEntryOverhead
		}
		if maxMemory -= int64(len(value)); maxMemory < 0 {
			return false
		}
	}
}

// lookup returns the value of the first header named name in the simple
// headers of the current part.
func (r *Reader) lookup(name string) ([]byte, bool) {
	if len(name) == len("Content-Disposition") && asciiEqualFold([]byte(name), "Content-Disposition") {
		return r.disposition, r.hasDisposition
	}
	block := r.header
	for {
		i := bytes.IndexByte(block, '\n')
		line := trimEOL(block[:i+1])
		block = block[i+1:]
		if len(line) == 0 {
			return nil, false
		}
		// simpleHeader validated the lines.
		if key, value, _ := bytes.Cut(line, []byte{':'}); asciiEqualFold(key, name) {
			return trimLWSP(value), true
		}
	}
}

// splitHeaderLine returns the key and the value of a header line, if
// net/textproto accepts it.
func splitHeaderLine(line []byte) (key, value []byte, ok bool) {
	key, value, ok = bytes.Cut(line, []byte{':'})
	if !ok || len(key) == 0 {
		return nil, nil, false
	}
	for _, c := range key {
		if !validHeaderFieldByte(c) {
			return nil, nil, false
		}
	}
	for _, c := range value {
		if !validHeaderValueByte(c) {
			return nil, nil, false
		}
	}
	return key, trimLWSP(value), true
}

// parseFormData parses a Content-Disposition like mime.ParseMediaType,
// without allocating. It returns the name and filename parameters if the
// type is "form-data". It reports false for the values mime.ParseMediaType
// must handle: invalid values, RFC 2231 parameters, quoted-pairs, and
// non-ASCII bytes outside of quoted strings, which unicode.IsSpace could
// match.
func parseFormData(v []byte) (formData bool, name, filename []byte, ok bool) {
	base := v
	if i := bytes.IndexByte(v, ';'); i >= 0 {
		base = v[:i]
	}
	for _, c := range base {
		if c >= 0x80 {
			return false, nil, nil, false
		}
	}
	if !asciiEqualFold(trimLWSP(base), "form-data") {
		return false, nil, nil, true
	}

	// Duplicate parameters are allowed if their values are equal.
	var params [8]struct{ key, value []byte }
	n := 0
	rest := v[len(base):]
	for {
		if rest, ok = skipLWSP(rest); !ok {
			return false, nil, nil, false
		}
		if len(rest) == 0 {
			break
		}
		if rest[0] != ';' {
			return false, nil, nil, false
		}
		if rest, ok = skipLWSP(rest[1:]); !ok {
			return false, nil, nil, false
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
			return false, nil, nil, false
		}
		if rest, ok = skipLWSP(rest[i:]); !ok || len(rest) == 0 || rest[0] != '=' {
			return false, nil, nil, false
		}
		if rest, ok = skipLWSP(rest[1:]); !ok || len(rest) == 0 {
			return false, nil, nil, false
		}

		var value []byte
		if rest[0] == '"' {
			end := bytes.IndexByte(rest[1:], '"')
			if end < 0 {
				return false, nil, nil, false
			}
			value, rest = rest[1:end+1], rest[end+2:]
			for _, c := range value {
				if c == '\\' || c == '\r' || c == '\n' {
					return false, nil, nil, false
				}
			}
		} else {
			i = 0
			for i < len(rest) && isTokenChar(rest[i]) {
				i++
			}
			if i == 0 {
				return false, nil, nil, false
			}
			value, rest = rest[:i], rest[i:]
		}

		duplicate := false
		for _, p := range params[:n] {
			if bytes.EqualFold(p.key, key) {
				if !bytes.Equal(p.value, value) {
					return false, nil, nil, false
				}
				duplicate = true
			}
		}
		if duplicate {
			continue
		}
		if n == len(params) {
			return false, nil, nil, false
		}
		params[n].key, params[n].value = key, value
		n++

		switch {
		case asciiEqualFold(key, "name"):
			name = value
		case asciiEqualFold(key, "filename"):
			filename = value
		}
	}
	return true, name, filename, true
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

// trimEOL removes the "\n" or "\r\n" ending a line, like bufio.Reader.ReadLine.
func trimEOL(line []byte) []byte {
	line = bytes.TrimSuffix(line, []byte{'\n'})
	return bytes.TrimSuffix(line, []byte{'\r'})
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

// validHeaderFieldByte is a copy of the net/textproto function.
func validHeaderFieldByte(c byte) bool {
	const mask = 0 |
		(1<<(10)-1)<<'0' |
		(1<<(26)-1)<<'a' |
		(1<<(26)-1)<<'A' |
		1<<'!' |
		1<<'#' |
		1<<'$' |
		1<<'%' |
		1<<'&' |
		1<<'\'' |
		1<<'*' |
		1<<'+' |
		1<<'-' |
		1<<'.' |
		1<<'^' |
		1<<'_' |
		1<<'`' |
		1<<'|' |
		1<<'~'
	return ((uint64(1)<<c)&(mask&(1<<64-1)) |
		(uint64(1)<<(c-64))&(mask>>64)) != 0
}

// validHeaderValueByte is a copy of the net/textproto function.
func validHeaderValueByte(c byte) bool {
	const mask = 0 |
		(1<<(0x7f-0x21)-1)<<0x21 | // VCHAR: %x21-7E
		1<<0x20 | // SP: %x20
		1<<0x09 // HTAB: %x09
	return ((uint64(1)<<c)&^(mask&(1<<64-1)) |
		(uint64(1)<<(c-64))&^(mask>>64)) == 0
}

// isTokenChar reports whether c is a token character of RFC 2045, like
// the mime function.
func isTokenChar(c byte) bool {
	// validHeaderFieldByte and the token of RFC 2045 differ by "{" and "}".
	return validHeaderFieldByte(c) || c == '{' || c == '}'
}
