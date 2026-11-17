package textenc

import (
	"bytes"
	"sync"
	"unicode"
	"unicode/utf8"
)

const (
	// detectSampleSize bounds how much of a file Detect inspects. Text
	// encodings are uniform, so a prefix is enough and huge files stay
	// cheap to read.
	detectSampleSize = 64 << 10

	// minConfidence is the average per-rune score a candidate decode
	// must reach. Correct decodes of CJK text land well above it; a
	// decode of the wrong encoding, or of binary data, lands below.
	minConfidence = 1.0

	// sparseInvalidShare is the fraction of invalid UTF-8 bytes below
	// which a file is treated as UTF-8 with a few damaged bytes. Such
	// files are far more likely to be mostly-UTF-8 than to be GBK with
	// a huge ASCII majority, and reading them as GBK would mangle the
	// parts that were fine.
	sparseInvalidShare = 1.0 / 200
)

// Detect returns the encoding data was written in. Files that are valid
// UTF-8, including pure ASCII, report UTF8: nothing needs converting.
// Unrecognised bytes report the zero Encoding, which callers must leave
// alone.
func Detect(data []byte) Encoding {
	if len(data) == 0 {
		return UTF8
	}
	if enc, ok := detectBOM(data); ok {
		return enc
	}
	// UTF-16 without a byte order mark can be valid UTF-8 (a NUL byte is
	// a legal UTF-8 code point), so it has to be ruled out first.
	if enc, ok := detectUTF16NoBOM(data); ok {
		return enc
	}
	if utf8.Valid(data) {
		return UTF8
	}
	if invalidShare(data) < sparseInvalidShare {
		return UTF8
	}
	return detectLegacy(data)
}

// detectBOM reports the encoding named by a leading byte order mark.
func detectBOM(data []byte) (Encoding, bool) {
	switch {
	case bytes.HasPrefix(data, utf8BOM.bom):
		return utf8BOM, true
	case bytes.HasPrefix(data, utf16LE.bom):
		// FF FE 00 00 would be UTF-32LE; Crush does not support it, and
		// treating it as UTF-16LE is the least surprising failure.
		return utf16LE, true
	case bytes.HasPrefix(data, utf16BE.bom):
		return utf16BE, true
	}
	return Encoding{}, false
}

// detectUTF16NoBOM spots BOM-less UTF-16 from its NUL-byte pattern: text
// that is mostly Latin or ASCII has a zero high byte in every code unit,
// so every other byte is zero while its neighbours are not. Fully CJK
// UTF-16 has no NULs at all and is not detected here; such files almost
// always carry a BOM, which detectBOM handles.
func detectUTF16NoBOM(data []byte) (Encoding, bool) {
	n := len(data) &^ 1
	if n < 8 {
		return Encoding{}, false
	}

	var evenNul, oddNul int
	for i := 0; i < n; i += 2 {
		if data[i] == 0 {
			evenNul++
		}
		if data[i+1] == 0 {
			oddNul++
		}
	}

	pairs := n / 2
	threshold := max(pairs/4, 1)
	var enc Encoding
	switch {
	case oddNul >= threshold && evenNul == 0:
		enc = utf16LEn
	case evenNul >= threshold && oddNul == 0:
		enc = utf16BEn
	default:
		return Encoding{}, false
	}
	if candidateScore(enc, data[:n]) < minConfidence {
		return Encoding{}, false
	}
	return enc, true
}

// detectLegacy scores every legacy candidate and returns the best one,
// or the zero Encoding when none of them makes sense of the bytes.
func detectLegacy(data []byte) Encoding {
	sample := data
	if len(sample) > detectSampleSize {
		sample = sample[:detectSampleSize]
	}

	best := Encoding{}
	bestScore := minConfidence
	for _, enc := range legacyCandidates {
		if score := candidateScore(enc, sample); score > bestScore {
			best, bestScore = enc, score
		}
	}
	return best
}

// candidateScore decodes sample with enc and rates how much the result
// looks like the text a source file would contain.
func candidateScore(enc Encoding, sample []byte) float64 {
	out, err := enc.x.NewDecoder().Bytes(sample)
	if err != nil && len(out) == 0 {
		return 0
	}
	return textScore(string(out))
}

// textScore averages a per-rune score over s. Common characters and
// ASCII score positive, replacement characters and controls score
// negative, and half-width katakana score negative because they are
// what Shift_JIS makes of GBK bytes.
func textScore(s string) float64 {
	var score float64
	total := 0
	for _, r := range s {
		total++
		switch {
		case r == utf8.RuneError:
			score -= 12
		case r == '\n' || r == '\r' || r == '\t':
			score += 2
		case r < 0x20 || r == 0x7F:
			score -= 10
		case r < 0x7F:
			score += 2
		case isHalfwidthKatakana(r):
			score -= 5
		case unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r):
			score += 8
		case unicode.Is(unicode.Hangul, r):
			score += 6
		case unicode.Is(unicode.Han, r) && isCommonHan(r):
			score += 6
		case unicode.Is(unicode.Han, r):
			// Rare ideographs decode out of every CJK encoding alike, so
			// they neither confirm nor refute a candidate.
		case isCJKPunctuation(r):
			score += 3
		case r >= 0xE000 && r <= 0xF8FF:
			score -= 10
		case unicode.IsControl(r):
			score -= 10
		}
	}
	if total == 0 {
		return 0
	}
	return score / float64(total)
}

// invalidShare returns the fraction of bytes that are not part of a
// valid UTF-8 sequence.
func invalidShare(data []byte) float64 {
	var bad int
	for i := 0; i < len(data); {
		r, size := utf8.DecodeRune(data[i:])
		if r == utf8.RuneError && size == 1 {
			bad++
		}
		i += size
	}
	return float64(bad) / float64(len(data))
}

func isHalfwidthKatakana(r rune) bool {
	return r >= 0xFF61 && r <= 0xFF9F
}

func isCJKPunctuation(r rune) bool {
	switch r {
	case '。', '、', '，', '．', '：', '；', '！', '？', '“', '”', '‘', '’',
		'（', '）', '【', '】', '「', '」', '『', '』', '～', '・', '·', '—',
		'…', '《', '》', '〈', '〉', '〔', '〕', '［', '］':
		return true
	}
	return false
}

// isCommonHan reports whether r is a character that appears often in
// Chinese or Japanese prose. Rare ideographs are exactly what a wrong
// decode produces, so only common ones count as evidence.
func isCommonHan(r rune) bool {
	commonHanOnce.Do(func() { commonHan = buildCommonHan() })
	_, ok := commonHan[r]
	return ok
}

var (
	commonHanOnce sync.Once
	commonHan     map[rune]struct{}
)

func buildCommonHan() map[rune]struct{} {
	set := make(map[rune]struct{}, (len(commonHanChars)+len(traditionalCommonHanChars))/3)
	for _, list := range []string{commonHanChars, traditionalCommonHanChars} {
		for _, r := range list {
			set[r] = struct{}{}
		}
	}
	return set
}

// commonHanChars lists frequent simplified and traditional Chinese
// characters plus the vocabulary of software development, which is what
// shows up in source comments and identifiers. Duplicates are harmless:
// the set builder ignores them.
const commonHanChars = `的一是在不了有和人这中大为上个国我以要他时来用们生到作地于出就分对成会可主发年动同工也能下过子说产种面而方后多定行学法所民得经十三之进着等部度家电力里如水化高自二理起小物现实加量都两体制机当使点从业本去把性好应开合还因由其些然前外天政四日那社义事平形相全表间样与关各重新线内数正心反你明看原又么利比或但质气第向道命此变条只没结解问意建月公无系军很情者最立代想已通并提直题程展五果料象员位入常文总次品式活设及管特件长求老头基资边流路级少图山统接知较将组见计别她手角期根论运农指几九区强放决西被干做必战先回则任取据处队南给色光门即保治北造百规热领七海口东导器压志世金增争济阶油思术极交受联什认六共权收证改清美再采转更单风切打白教速花带安场身车例真务具万每目至达走积示议声报斗完类八离华名确才科张信马节话米整空元况今集温传土许步群广石记需段研界拉林律叫且究观越织装影算低持音众书布复容儿须际商非验连断深难近矿千周委素技备半办青省列习响约支般史感劳便团往酸历市克何除消构府称太准精值号率族维划选标写存候毛亲快效斯院查江型眼王按格养易置派层片始却专状育厂京识适属圆包火住调满县局照参红细引听该铁价严龙飞`

// traditionalCommonHanChars are the traditional forms that never appear
// in simplified prose. Without them a Big5 file can tie with a wrong
// GB18030 decode of the same bytes.
const traditionalCommonHanChars = `這個們時為會說對過來發現沒樣麼應該開關學經歷進實際網路電話機車錢買賣讀寫點線圖檔區塊讓與從兩萬據聲響錯誤調變數類型條件執行編譯訊資料庫應用程式處理檢查確認設定選擇顯示隱藏載入儲存刪除修改添加查詢更新提交回滾版本控制分支合併衝突解決依賴套件構建運行部署伺服器客戶端請求響應超時重試緩存佇列訊息事件監聽註冊回調異步同步並發鎖定死鎖資源釋放記憶體洩漏效能優化偵錯堆疊位址引用空值判斷轉換序列化編解碼加密解密權限認證授權令牌使用者密碼會話角色選單頁面按鈕輸入表單驗證提示彈出視窗清單排序分頁過濾匯出匯入文章圖片上傳下載路徑資料夾目錄大小格式時間日期解析`
