// Package textenc detects the character encoding of text files and
// converts between that encoding and UTF-8.
//
// Source trees that predate UTF-8 are still common: Windows editors save
// GBK, older Japanese tooling saves Shift_JIS, and a few Windows tools
// write UTF-16. Read as raw bytes those files reach the model as
// mojibake, and writing UTF-8 back to them silently changes the file's
// encoding. Detect picks the encoding from the bytes, and Encoding
// converts in both directions so a file keeps the encoding it came with.
//
// Only UTF-8 is read and written natively. Everything else is decoded
// on the way in and re-encoded on the way out.
package textenc

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

// Encoding is a character encoding a text file can be stored in. The
// zero value means "unknown": bytes that no candidate explained. Callers
// must not convert a file whose encoding is unknown.
type Encoding struct {
	name string
	// x is nil for UTF-8, which needs no conversion.
	x   encoding.Encoding
	bom []byte
}

var (
	// UTF8 is the encoding Crush reads and writes natively. Files that
	// are already UTF-8 are never converted.
	UTF8 = Encoding{name: "UTF-8"}

	utf8BOM  = Encoding{name: "UTF-8", bom: []byte{0xEF, 0xBB, 0xBF}}
	utf16LE  = Encoding{name: "UTF-16LE", x: unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM), bom: []byte{0xFF, 0xFE}}
	utf16BE  = Encoding{name: "UTF-16BE", x: unicode.UTF16(unicode.BigEndian, unicode.IgnoreBOM), bom: []byte{0xFE, 0xFF}}
	utf16LEn = Encoding{name: "UTF-16LE", x: unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM)}
	utf16BEn = Encoding{name: "UTF-16BE", x: unicode.UTF16(unicode.BigEndian, unicode.IgnoreBOM)}

	// gb18030 covers GBK: every GBK file decodes identically, and the
	// four-byte extension lets a write keep characters GBK cannot.
	gb18030  = Encoding{name: "GB18030", x: simplifiedchinese.GB18030}
	shiftJIS = Encoding{name: "Shift_JIS", x: japanese.ShiftJIS}
	big5     = Encoding{name: "Big5", x: traditionalchinese.Big5}
)

// legacyCandidates are the encodings Detect scores when a file is not
// valid UTF-8. They are the encodings that dominate pre-UTF-8 source
// trees in East Asia.
var legacyCandidates = []Encoding{gb18030, shiftJIS, big5}

// Name returns the encoding's name, or "unknown" for the zero value.
func (e Encoding) Name() string {
	if e.name == "" {
		return "unknown"
	}
	return e.name
}

// Valid reports whether the encoding was identified.
func (e Encoding) Valid() bool {
	return e.name != ""
}

// IsUTF8 reports whether no conversion is needed. It is true for UTF-8
// with and without a byte order mark.
func (e Encoding) IsUTF8() bool {
	return e.Valid() && e.x == nil
}

// Decode converts data to a string and reports the encoding it was
// written in. Detect always runs first, so callers never have to decide
// which encoding a file uses.
//
// Invalid UTF-8 bytes in an otherwise UTF-8 file become U+FFFD, which
// matches what an editor shows, rather than failing the read.
func Decode(data []byte) (string, Encoding) {
	enc := Detect(data)
	body := bytes.TrimPrefix(data, enc.bom)
	if enc.IsUTF8() {
		return strings.ToValidUTF8(string(body), "\uFFFD"), enc
	}
	if !enc.Valid() {
		return string(data), enc
	}
	out, err := enc.x.NewDecoder().Bytes(body)
	if err != nil && len(out) == 0 {
		// The bytes were not in this encoding after all.
		return string(data), Encoding{}
	}
	return string(out), enc
}

// ReadFile reads path and returns its text in UTF-8 together with the
// encoding it was stored in.
func ReadFile(path string) (string, Encoding, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", Encoding{}, err
	}
	text, enc := Decode(data)
	return text, enc, nil
}

// Open opens path for reading and returns a reader that yields UTF-8 no
// matter what the file is encoded in, along with the detected encoding.
// For UTF-8 files the reader is the file itself.
func Open(path string) (io.ReadCloser, Encoding, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, Encoding{}, err
	}

	head := make([]byte, detectSampleSize)
	n, err := io.ReadFull(f, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		_ = f.Close()
		return nil, Encoding{}, err
	}
	enc := Detect(head[:n])

	// Skip the byte order mark: it is metadata for the reader, not part
	// of the text.
	if _, err := f.Seek(int64(len(enc.bom)), io.SeekStart); err != nil {
		_ = f.Close()
		return nil, Encoding{}, err
	}
	if enc.IsUTF8() || !enc.Valid() {
		return f, enc, nil
	}
	return &decodedReader{Reader: transform.NewReader(f, enc.x.NewDecoder()), file: f}, enc, nil
}

// Encode converts s to the encoding's bytes, keeping the byte order mark
// the encoding was detected with. Characters the encoding cannot
// represent are written as "?" and counted: silently dropping them or
// switching the whole file to UTF-8 would lose more than a visible "?".
func (e Encoding) Encode(s string) (data []byte, replaced int, err error) {
	if !e.Valid() {
		e = UTF8
	}
	if e.IsUTF8() {
		out := make([]byte, 0, len(e.bom)+len(s))
		out = append(out, e.bom...)
		return append(out, s...), 0, nil
	}

	body, err := e.x.NewEncoder().Bytes([]byte(s))
	if err != nil {
		// One unrepresentable character must not fail the whole write;
		// convert rune by rune and substitute the offenders.
		enc := e.x.NewEncoder()
		body = body[:0]
		for _, r := range s {
			chunk, runeErr := enc.Bytes([]byte(string(r)))
			if runeErr != nil {
				body = append(body, '?')
				replaced++
				continue
			}
			body = append(body, chunk...)
		}
	}
	out := make([]byte, 0, len(e.bom)+len(body))
	out = append(out, e.bom...)
	return append(out, body...), replaced, nil
}

// decodedReader adapts a transforming reader to io.ReadCloser by
// delegating Close to the underlying file.
type decodedReader struct {
	io.Reader
	file *os.File
}

func (r *decodedReader) Close() error { return r.file.Close() }
