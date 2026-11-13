// Copyright 2023 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package multipart

import (
	"net/textproto"
	_ "unsafe" // for go:linkname
)

// readMIMEHeader is defined in package [net/textproto].
//
//go:linkname readMIMEHeader net/textproto.readMIMEHeader
func readMIMEHeader(r *textproto.Reader, maxMemory, maxHeaders int64) (textproto.MIMEHeader, error)

// readMIMEHeaderInto is defined in package [net/textproto].
//
//go:linkname readMIMEHeaderInto net/textproto.readMIMEHeaderInto
func readMIMEHeaderInto(r *textproto.Reader, m textproto.MIMEHeader, strs []string, maxMemory, maxHeaders int64) error
