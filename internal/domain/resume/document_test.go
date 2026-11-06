package resume

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestExtractRejectsUnsupportedAndImageTypes(t *testing.T) {
	cases := []struct {
		name string
		want error
	}{
		{"resume.exe", ErrUnsupportedFileType},
		{"resume", ErrUnsupportedFileType},
		{"resume.png", ErrImageRequiresOCR},
		{"resume.JPG", ErrImageRequiresOCR},
		{"resume.jpeg", ErrImageRequiresOCR},
	}
	for _, tc := range cases {
		if _, err := Extract(tc.name, []byte("hello")); !errors.Is(err, tc.want) {
			t.Fatalf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
}

func TestExtractRejectsOversizedFile(t *testing.T) {
	data := make([]byte, MaxDocumentSize+1)
	if _, err := Extract("resume.txt", data); !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("err = %v, want ErrFileTooLarge", err)
	}
}

func TestExtractPlainTextAndProfile(t *testing.T) {
	text := "5 年 Go 后端经验，负责订单履约链路\n" +
		"技能：Go、MySQL、Redis\n" +
		"项目：订单幂等改造，用本地消息表保证最终一致性\n"
	for _, name := range []string{"resume.txt", "resume.md"} {
		doc, err := Extract(name, []byte(text))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if doc.Format != FormatTXT && doc.Format != FormatMD {
			t.Fatalf("%s: format = %s", name, doc.Format)
		}
		if doc.Chars == 0 || !strings.Contains(doc.Text, "本地消息表") {
			t.Fatalf("%s: text = %q", name, doc.Text)
		}
		profile := Parse(doc.Text)
		profile.Normalize()
		if profile.Years != 5 {
			t.Fatalf("%s: years = %d, want 5", name, profile.Years)
		}
		if len(profile.Skills) == 0 || len(profile.Projects) == 0 {
			t.Fatalf("%s: profile = %+v", name, profile)
		}
	}
}

func TestExtractEmptyPlainTextFails(t *testing.T) {
	if _, err := Extract("resume.txt", []byte("   \n\t\n")); !errors.Is(err, ErrExtractFailed) {
		t.Fatalf("err = %v, want ErrExtractFailed", err)
	}
}

func TestExtractDOCX(t *testing.T) {
	docx := buildDOCX(`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>` +
		`<w:p><w:r><w:t>3 年 Go 后端经验</w:t></w:r></w:p>` +
		`<w:p><w:r><w:t>项目：订单履约</w:t></w:r><w:r><w:t>，Redis 缓存</w:t></w:r></w:p>` +
		`</w:body></w:document>`)

	doc, err := Extract("resume.docx", docx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doc.Text, "3 年 Go 后端经验") || !strings.Contains(doc.Text, "订单履约，Redis 缓存") {
		t.Fatalf("text = %q", doc.Text)
	}
	if profile := Parse(doc.Text); profile.Years != 3 {
		t.Fatalf("years = %d, want 3", profile.Years)
	}
}

func TestExtractCorruptDOCXReportsParseFailure(t *testing.T) {
	if _, err := Extract("resume.docx", []byte("not a zip")); !errors.Is(err, ErrExtractFailed) {
		t.Fatalf("err = %v, want ErrExtractFailed", err)
	}

	// A valid zip without word/document.xml is still unusable.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("readme.txt")
	_, _ = w.Write([]byte("hello"))
	_ = zw.Close()
	if _, err := Extract("resume.docx", buf.Bytes()); !errors.Is(err, ErrExtractFailed) {
		t.Fatalf("err = %v, want ErrExtractFailed", err)
	}
}

func TestExtractPDF(t *testing.T) {
	doc, err := Extract("resume.pdf", buildPDF("5 years Go backend order system"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doc.Text, "5 years Go backend") {
		t.Fatalf("text = %q", doc.Text)
	}
	if _, err := Extract("broken.pdf", []byte("not a pdf")); !errors.Is(err, ErrExtractFailed) {
		t.Fatalf("err = %v, want ErrExtractFailed", err)
	}
}

func buildDOCX(documentXML string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("word/document.xml")
	_, _ = w.Write([]byte(documentXML))
	_ = zw.Close()
	return buf.Bytes()
}

// buildPDF writes a minimal single-page PDF with one text run. Offsets are
// tracked while writing so the xref table is valid.
func buildPDF(text string) []byte {
	var b bytes.Buffer
	var offsets []int
	b.WriteString("%PDF-1.4\n")
	add := func(num int, body string) {
		offsets = append(offsets, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", num, body)
	}
	add(1, "<< /Type /Catalog /Pages 2 0 R >>")
	add(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	add(3, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>")
	// Fixed 600-unit widths for every printable char make the reader advance X
	// deterministically, so spaces can be reconstructed from the gaps.
	widths := strings.TrimSpace(strings.Repeat("600 ", 95))
	add(4, "<< /Type /Font /Subtype /Type1 /BaseFont /Courier /FirstChar 32 /LastChar 126 /Widths ["+widths+"] >>")
	stream := fmt.Sprintf("BT /F1 12 Tf 72 720 Td (%s) Tj ET", text)
	add(5, fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream))

	xref := b.Len()
	b.WriteString("xref\n0 6\n0000000000 65535 f \n")
	for _, off := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size 6 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", xref)
	return b.Bytes()
}
