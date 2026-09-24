package builtin

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ai-edr/internal/executor"
)

func TestDocumentParseDOCX(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.docx")
	files := map[string]string{
		"docProps/core.xml": `<?xml version="1.0"?><cp:coreProperties xmlns:cp="x" xmlns:dc="x"><dc:title>Case Notes</dc:title><dc:creator>DeepSentry</dc:creator></cp:coreProperties>`,
		"word/document.xml": `<?xml version="1.0"?><w:document xmlns:w="w"><w:body><w:p><w:r><w:t>Hello 世界</w:t></w:r></w:p><w:tbl><w:tr><w:tc><w:p><w:r><w:t>A1</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>B1</w:t></w:r></w:p></w:tc></w:tr></w:tbl></w:body></w:document>`,
		"word/comments.xml": `<w:comments xmlns:w="w"><w:comment><w:p><w:r><w:t>review comment</w:t></w:r></w:p></w:comment></w:comments>`,
	}
	writeTestZip(t, path, files)
	withLocalExecutor(t)

	out, err := DocumentParse(Runtime{}, path, "text", 20000, 100, 8)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Word DOCX", "title=Case Notes", "Hello 世界", "A1", "review comment"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in output:\n%s", want, out)
		}
	}
}

func TestDocumentParseXLSX(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "book.xlsx")
	files := map[string]string{
		"xl/workbook.xml":            `<workbook xmlns:r="r"><sheets><sheet name="Evidence" sheetId="1" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="rId1" Target="/xl/worksheets/sheet1.xml"/></Relationships>`,
		"xl/sharedStrings.xml":       `<sst><si><t>host</t></si><si><t>status</t></si><si><t>web-01</t></si></sst>`,
		"xl/worksheets/sheet1.xml":   `<worksheet><sheetData><row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c></row><row r="2"><c r="A2" t="s"><v>2</v></c><c r="B2"><f>1+1</f><v>2</v></c></row></sheetData></worksheet>`,
		"docProps/core.xml":          `<cp:coreProperties xmlns:cp="x"><title>Workbook</title></cp:coreProperties>`,
		"[Content_Types].xml":        `<Types/>`,
		"_rels/.rels":                `<Relationships/>`,
	}
	writeTestZip(t, path, files)
	withLocalExecutor(t)

	out, err := DocumentParse(Runtime{}, path, "tables", 20000, 100, 8)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Excel XLSX", "Sheet \"Evidence\"", "A=host", "B=status", "A=web-01", "=1+1 => 2"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in output:\n%s", want, out)
		}
	}
}

func TestDocumentParsePDF(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "doc.pdf")
	pdf := `%PDF-1.4
1 0 obj << /Type /Catalog /Pages 2 0 R >> endobj
2 0 obj << /Type /Pages /Count 1 /Kids [3 0 R] >> endobj
3 0 obj << /Type /Page /Contents 4 0 R >> endobj
4 0 obj << /Length 44 >>
stream
BT /F1 12 Tf 72 720 Td (Hello PDF) Tj ET
endstream
endobj
trailer << /Info << /Title (PDF Case) >> >>
%%EOF`
	if err := os.WriteFile(path, []byte(pdf), 0o644); err != nil {
		t.Fatal(err)
	}
	withLocalExecutor(t)

	out, err := DocumentParse(Runtime{}, path, "auto", 20000, 100, 8)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "PDF document") || !strings.Contains(out, "Hello PDF") || !strings.Contains(out, "title=PDF Case") {
		t.Fatalf("bad pdf output:\n%s", out)
	}
}

func TestDocumentParseLegacyOfficeFallbackUTF16(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "old.xls")
	data := []byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1}
	for _, r := range "flag{office_binary}" {
		data = append(data, byte(r), 0)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	withLocalExecutor(t)

	out, err := DocumentParse(Runtime{}, path, "text", 20000, 100, 8)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Excel XLS") || !strings.Contains(out, "flag{office_binary}") {
		t.Fatalf("bad legacy output:\n%s", out)
	}
}

func writeTestZip(t *testing.T, dst string, files map[string]string) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	for _, name := range names {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(files[name])); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func withLocalExecutor(t *testing.T) {
	t.Helper()
	old := executor.Current
	executor.Current = &executor.LocalExecutor{}
	t.Cleanup(func() { executor.Current = old })
}
