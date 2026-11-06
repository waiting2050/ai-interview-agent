package resume

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
	"rsc.io/pdf"
)

// MaxDocumentSize is the upload limit for a resume attachment (5 MiB).
const MaxDocumentSize = 5 << 20

// maxTextRunes caps extracted text so an oversized attachment cannot bloat the
// session payload. Real resumes are far below this.
const maxTextRunes = 100000

// DocumentFormat is the detected attachment format. Only the text formats below
// are parsed locally; images are reserved for a future OCR/multimodal path.
type DocumentFormat string

const (
	FormatTXT  DocumentFormat = "txt"
	FormatMD   DocumentFormat = "md"
	FormatDOCX DocumentFormat = "docx"
	FormatPDF  DocumentFormat = "pdf"
	FormatPNG  DocumentFormat = "png"
	FormatJPG  DocumentFormat = "jpg"
)

var (
	// ErrUnsupportedFileType marks extensions outside the whitelist.
	ErrUnsupportedFileType = errors.New("unsupported resume file type")
	// ErrImageRequiresOCR marks PNG/JPG uploads: the branch is reserved but not
	// implemented in this milestone.
	ErrImageRequiresOCR = errors.New("image resume requires OCR")
	// ErrFileTooLarge marks attachments above MaxDocumentSize.
	ErrFileTooLarge = errors.New("resume file too large")
	// ErrExtractFailed marks files that could not yield usable text.
	ErrExtractFailed = errors.New("resume text extraction failed")
)

// Document is the result of local extraction. Text is the plain resume text
// handed to Parse; Profile is filled by the caller.
type Document struct {
	Filename  string         `json:"filename"`
	Format    DocumentFormat `json:"format"`
	Bytes     int            `json:"bytes"`
	Chars     int            `json:"chars"`
	Truncated bool           `json:"truncated"`
	Text      string         `json:"text"`
}

var formatByExt = map[string]DocumentFormat{
	".txt": FormatTXT,
	".md":  FormatMD,
	".docx": FormatDOCX,
	".pdf": FormatPDF,
	".png": FormatPNG,
	".jpg": FormatJPG,
	".jpeg": FormatJPG,
}

// Extract reads a resume attachment fully in memory and returns its plain text.
// Nothing here calls a model or the network.
func Extract(filename string, data []byte) (Document, error) {
	if len(data) > MaxDocumentSize {
		return Document{}, fmt.Errorf("%w: %d bytes exceeds %d", ErrFileTooLarge, len(data), MaxDocumentSize)
	}
	format, ok := formatByExt[strings.ToLower(filepath.Ext(filename))]
	if !ok {
		return Document{}, fmt.Errorf("%w: %s", ErrUnsupportedFileType, filepath.Ext(filename))
	}
	if format == FormatPNG || format == FormatJPG {
		return Document{}, fmt.Errorf("%w: %s", ErrImageRequiresOCR, format)
	}
	if len(data) == 0 {
		return Document{}, fmt.Errorf("%w: file is empty", ErrExtractFailed)
	}

	var (
		raw string
		err error
	)
	switch format {
	case FormatTXT, FormatMD:
		raw, err = decodePlainText(data)
	case FormatDOCX:
		raw, err = extractDOCX(data)
	case FormatPDF:
		raw, err = extractPDF(data)
	}
	if err != nil {
		return Document{}, err
	}

	text, truncated := normalizeText(raw)
	if strings.TrimSpace(text) == "" {
		return Document{}, fmt.Errorf("%w: no usable text", ErrExtractFailed)
	}
	return Document{
		Filename:  filename,
		Format:    format,
		Bytes:     len(data),
		Chars:     utf8.RuneCountInString(text),
		Truncated: truncated,
		Text:      text,
	}, nil
}

// decodePlainText accepts UTF-8 and falls back to GB18030, which is still
// common for Chinese resumes exported from older office suites.
func decodePlainText(data []byte) (string, error) {
	if utf8.Valid(data) {
		return string(data), nil
	}
	decoded, err := simplifiedchinese.GB18030.NewDecoder().Bytes(data)
	if err != nil {
		return "", fmt.Errorf("%w: 文件不是 UTF-8 或 GB18030 编码", ErrExtractFailed)
	}
	return string(decoded), nil
}

// extractDOCX reads word/document.xml and joins paragraphs. Runs inside
// w:t elements are concatenated; paragraph ends and explicit breaks become
// newlines. Deleted text (w:delText) and field instructions are ignored.
func extractDOCX(data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("%w: DOCX 压缩包损坏", ErrExtractFailed)
	}
	var entry *zip.File
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			entry = f
			break
		}
	}
	if entry == nil {
		return "", fmt.Errorf("%w: DOCX 缺少 word/document.xml", ErrExtractFailed)
	}
	rc, err := entry.Open()
	if err != nil {
		return "", fmt.Errorf("%w: 无法读取 DOCX 正文", ErrExtractFailed)
	}
	defer rc.Close()

	var b strings.Builder
	dec := xml.NewDecoder(rc)
	inText := false
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("%w: DOCX 正文 XML 无法解析", ErrExtractFailed)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "t":
				inText = true
			case "tab":
				b.WriteByte('\t')
			case "br", "cr":
				b.WriteByte('\n')
			}
		case xml.CharData:
			if inText {
				b.Write(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inText = false
			case "p":
				b.WriteByte('\n')
			}
		}
	}
	return b.String(), nil
}

// extractPDF walks every page, sorts runs reading-order (top to bottom, left to
// right) and joins runs on the same baseline. Scanned PDFs and PDFs using font
// encodings the pure-Go reader cannot decode yield no text; callers treat that
// as a parse failure and ask the candidate to paste the resume instead.
func extractPDF(data []byte) (text string, err error) {
	defer func() {
		if r := recover(); r != nil {
			text, err = "", fmt.Errorf("%w: PDF 结构无法解析", ErrExtractFailed)
		}
	}()

	reader, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("%w: PDF 文件损坏或已加密", ErrExtractFailed)
	}

	var b strings.Builder
	for page := 1; page <= reader.NumPage(); page++ {
		p := reader.Page(page)
		if p.V.IsNull() {
			continue
		}
		content := p.Content()
		runs := content.Text
		sort.Sort(pdf.TextVertical(runs))

		prevIndex := -1
		for i, run := range runs {
			s := strings.TrimSpace(run.S)
			if s == "" {
				continue
			}
			if prevIndex >= 0 {
				prev := runs[prevIndex]
				if math.Abs(run.Y-prev.Y) > 2 {
					b.WriteByte('\n')
				} else if run.X-(prev.X+prev.W) > 1.5 {
					b.WriteByte(' ')
				}
			}
			b.WriteString(s)
			prevIndex = i
		}
		b.WriteByte('\n')
	}
	return b.String(), nil
}

// normalizeText strips NULs, normalises line endings, drops trailing spaces and
// collapses runs of blank lines. It reports whether the text was capped.
func normalizeText(raw string) (string, bool) {
	raw = strings.ReplaceAll(raw, "\x00", "")
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.ReplaceAll(raw, "\r", "\n")

	lines := strings.Split(raw, "\n")
	out := make([]string, 0, len(lines))
	blank := 0
	for _, line := range lines {
		line = strings.TrimRight(line, " \t")
		if line == "" {
			blank++
			if blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, line)
	}
	text := strings.TrimSpace(strings.Join(out, "\n"))

	runes := []rune(text)
	if len(runes) > maxTextRunes {
		return strings.TrimSpace(string(runes[:maxTextRunes])) + "\n…（已截断）", true
	}
	return text, false
}
