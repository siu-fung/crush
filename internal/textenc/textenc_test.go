package textenc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// encodeWith is the test-side equivalent of a file some other editor
// wrote: it produces bytes in enc without any detection involved.
func encodeWith(t *testing.T, enc Encoding, text string) []byte {
	t.Helper()
	data, replaced, err := enc.Encode(text)
	require.NoError(t, err)
	require.Zero(t, replaced, "test fixture must be representable in %s", enc.Name())
	return data
}

func TestDetectRoundTrip(t *testing.T) {
	t.Parallel()

	const (
		chinese     = "这是一个中文测试文件，用来验证编码检测是否可靠。\n"
		japanese    = "これは日本語のテストファイルです。エンコードの検出を確認します。\n"
		traditional = "這是一個繁體中文測試檔案，用來驗證編碼偵測。\n"
	)
	tests := []struct {
		name string
		enc  Encoding
		text string
	}{
		{"utf8", UTF8, chinese},
		{"utf8 with bom", utf8BOM, chinese},
		{"utf16le with bom", utf16LE, chinese},
		{"utf16be with bom", utf16BE, japanese},
		{"gb18030", gb18030, chinese},
		{"shift_jis", shiftJIS, japanese},
		{"big5", big5, traditional},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			data := encodeWith(t, tt.enc, tt.text)

			text, detected := Decode(data)
			require.Equal(t, tt.enc.Name(), detected.Name())
			require.Equal(t, tt.text, text)

			// Writing back must reproduce the original bytes, byte order
			// mark included.
			back, replaced, err := detected.Encode(text)
			require.NoError(t, err)
			require.Zero(t, replaced)
			require.Equal(t, data, back)
		})
	}
}

func TestDetectKeepsASCIIAndUTF8Unconverted(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		data []byte
	}{
		{"empty", nil},
		{"ascii", []byte("package main\n\nfunc main() {}\n")},
		{"utf8", []byte("// 中文注释\n")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			enc := Detect(tt.data)
			require.Equal(t, UTF8.Name(), enc.Name())
			require.True(t, enc.IsUTF8())
		})
	}
}

// A file that is UTF-8 apart from a stray byte is still UTF-8: reading
// it as GBK would mangle every character around the damage.
func TestDetectSparseInvalidBytesStayUTF8(t *testing.T) {
	t.Parallel()

	data := []byte(strings.Repeat("// 正常运行的中文注释\n", 200) + "\xff\n")

	enc := Detect(data)
	require.True(t, enc.IsUTF8(), "a single stray byte must not flip the encoding, got %s", enc.Name())

	text, detected := Decode(data)
	require.True(t, detected.IsUTF8())
	require.Contains(t, text, "\uFFFD")
}

func TestDetectUTF16WithoutBOM(t *testing.T) {
	t.Parallel()

	const text = "package main\n// 中文\n"
	for _, enc := range []Encoding{utf16LEn, utf16BEn} {
		data := encodeWith(t, enc, text)

		decoded, detected := Decode(data)
		require.Equal(t, enc.Name(), detected.Name())
		require.Equal(t, text, decoded)
	}
}

func TestDetectUnknownForBinary(t *testing.T) {
	t.Parallel()

	data := make([]byte, 4096)
	for i := range data {
		data[i] = byte((i * 7) % 251)
	}

	enc := Detect(data)
	require.False(t, enc.IsUTF8(), "binary bytes must not be reported as UTF-8")
	require.False(t, enc.Valid(), "binary bytes must not be reported as a text encoding, got %s", enc.Name())
}

func TestEncodeCountsUnrepresentable(t *testing.T) {
	t.Parallel()

	data, replaced, err := shiftJIS.Encode("日本語 \U0001F600 です")
	require.NoError(t, err)
	require.Equal(t, 1, replaced, "the emoji has no Shift_JIS mapping")

	text, detected := Decode(data)
	require.Equal(t, shiftJIS.Name(), detected.Name())
	require.Equal(t, "日本語 ? です", text)
}

func TestOpenDecodesNonUTF8File(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "comments.c")
	const text = "/* 中文注释 */\nint main(void) { return 0; }\n"
	require.NoError(t, os.WriteFile(path, encodeWith(t, gb18030, text), 0o644))

	r, enc, err := Open(path)
	require.NoError(t, err)
	defer r.Close()

	require.Equal(t, gb18030.Name(), enc.Name())

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotEqual(t, text, string(got), "fixture must really be non-UTF-8")

	buf := make([]byte, len(text))
	n, err := r.Read(buf)
	require.NoError(t, err)
	require.Equal(t, text, string(buf[:n]))
}

func TestReadFileReportsEncoding(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "notes.txt")
	const text = "日本語のメモ\n"
	require.NoError(t, os.WriteFile(path, encodeWith(t, shiftJIS, text), 0o644))

	got, enc, err := ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, shiftJIS.Name(), enc.Name())
	require.Equal(t, text, got)
}
