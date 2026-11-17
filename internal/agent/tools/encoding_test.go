package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
	"unicode/utf8"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/permission/testutil"
	"github.com/stretchr/testify/require"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/simplifiedchinese"
)

// writeGBK encodes text as GBK, standing in for a file saved by a
// Windows editor.
func writeGBK(t *testing.T, text string) []byte {
	t.Helper()
	data, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(text))
	require.NoError(t, err)
	require.False(t, utf8.Valid(data), "GBK fixture must not be valid UTF-8")
	return data
}

// readGBK decodes bytes with GBK so a test can assert what an editor on
// the original platform would show.
func readGBK(t *testing.T, data []byte) string {
	t.Helper()
	text, err := simplifiedchinese.GBK.NewDecoder().Bytes(data)
	require.NoError(t, err)
	return string(text)
}

// shiftJISFixture encodes text as Shift_JIS for the read-path tests.
func shiftJISFixture(t *testing.T, text string) ([]byte, error) {
	t.Helper()
	data, err := japanese.ShiftJIS.NewEncoder().Bytes([]byte(text))
	require.NoError(t, err)
	return data, nil
}

func TestReadTextFileDecodesGBK(t *testing.T) {
	t.Parallel()

	filePath := filepath.Join(t.TempDir(), "comments.c")
	const text = "/* 中文注释，说明这段代码 */\nint main(void) { return 0; }\n"
	require.NoError(t, os.WriteFile(filePath, writeGBK(t, text), 0o644))

	content, encoding, hasMore, err := readTextFile(filePath, 0, 10, 0)
	require.NoError(t, err)
	require.False(t, hasMore)
	require.Equal(t, "GB18030", encoding.Name())
	require.Equal(t, text, content)
}

func TestReadTextFileDecodesShiftJIS(t *testing.T) {
	t.Parallel()

	filePath := filepath.Join(t.TempDir(), "notes.txt")
	const text = "日本語のコメントです。\n"
	data, err := shiftJISFixture(t, text)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filePath, data, 0o644))

	content, encoding, _, err := readTextFile(filePath, 0, 10, 0)
	require.NoError(t, err)
	require.Equal(t, "Shift_JIS", encoding.Name())
	require.Equal(t, text, content)
}

func TestReplaceContentKeepsOriginalEncoding(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	filePath := filepath.Join(dir, "greeting.txt")
	require.NoError(t, os.WriteFile(filePath, writeGBK(t, "你好世界\n"), 0o644))

	edit := editContext{
		ctx:         context.WithValue(t.Context(), SessionIDContextKey, "session"),
		permissions: &mockPermissionService{},
		files:       &mockHistoryService{},
		filetracker: &mockEditFileTracker{lastRead: time.Now().Add(time.Second)},
		workingDir:  dir,
	}

	resp, err := replaceContent(edit, filePath, "世界", "朋友", false, fantasy.ToolCall{ID: "call"})
	require.NoError(t, err)
	require.False(t, resp.IsError)
	require.Contains(t, resp.Content, "GB18030")

	onDisk, err := os.ReadFile(filePath)
	require.NoError(t, err)
	require.False(t, utf8.Valid(onDisk), "the file must stay GBK, not be rewritten as UTF-8")
	require.Equal(t, "你好朋友\n", readGBK(t, onDisk))
}

func TestWriteToolKeepsExistingEncoding(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	filePath := filepath.Join(workingDir, "config.txt")
	require.NoError(t, os.WriteFile(filePath, writeGBK(t, "键=值\n"), 0o644))

	ctx := context.WithValue(context.Background(), SessionIDContextKey, "session")
	tool := NewWriteTool(nil, &testutil.MockPermissionService{}, &mockHistoryService{}, mockFileTrackerService{}, workingDir)

	input, err := json.Marshal(WriteParams{FilePath: "config.txt", Content: "键=新值\n"})
	require.NoError(t, err)

	resp, err := tool.Run(ctx, fantasy.ToolCall{ID: "call", Name: WriteToolName, Input: string(input)})
	require.NoError(t, err)
	require.False(t, resp.IsError)
	require.Contains(t, resp.Content, "GB18030")

	onDisk, err := os.ReadFile(filePath)
	require.NoError(t, err)
	require.False(t, utf8.Valid(onDisk), "overwriting must not convert the file to UTF-8")
	require.Equal(t, "键=新值\n", readGBK(t, onDisk))
}

func TestWriteToolCreatesNewFilesAsUTF8(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "session")
	tool := NewWriteTool(nil, &testutil.MockPermissionService{}, &mockHistoryService{}, mockFileTrackerService{}, workingDir)

	input, err := json.Marshal(WriteParams{FilePath: "new.txt", Content: "新文件\n"})
	require.NoError(t, err)

	resp, err := tool.Run(ctx, fantasy.ToolCall{ID: "call", Name: WriteToolName, Input: string(input)})
	require.NoError(t, err)
	require.False(t, resp.IsError)
	require.NotContains(t, resp.Content, "original encoding", "a new file needs no conversion note")

	onDisk, err := os.ReadFile(filepath.Join(workingDir, "new.txt"))
	require.NoError(t, err)
	require.Equal(t, "新文件\n", string(onDisk))
}
