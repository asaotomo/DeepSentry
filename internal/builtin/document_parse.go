package builtin

import (
	"archive/zip"
	"bytes"
	"compress/zlib"
	"encoding/csv"
	"encoding/xml"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
)

const (
	maxDocumentParseRead = 20 << 20
	maxOOXMLEntryRead    = 12 << 20
	maxPDFStreamRead     = 4 << 20
	maxBinaryStringHits  = 600
	maxXLSXColsPreview   = 60
)

type documentParseResult struct {
	Format   string
	Parser   string
	Meta     []string
	Text     string
	Tables   []string
	Warnings []string
}

// DocumentParse extracts text, tables, and metadata from common flow/layout
// documents without depending on target-side office/pdf commands.
func DocumentParse(rt Runtime, filePath, mode string, maxText, maxRows, maxSheets int) (string, error) {
	filePath = strings.TrimSpace(filePath)
	if filePath == "" {
		return "", fmt.Errorf("path 不能为空")
	}
	mode = normalizeDocumentMode(mode)
	if maxText <= 0 {
		maxText = 30000
	}
	if maxText > 100000 {
		maxText = 100000
	}
	if maxRows <= 0 {
		maxRows = 120
	}
	if maxRows > 1000 {
		maxRows = 1000
	}
	if maxSheets <= 0 {
		maxSheets = 8
	}
	if maxSheets > 50 {
		maxSheets = 50
	}

	data, err := readTargetLimited(filePath, maxDocumentParseRead)
	if err != nil {
		return "", err
	}
	result, err := parseDocumentBytes(filePath, data, maxText, maxRows, maxSheets)
	if err != nil {
		return "", err
	}
	return formatDocumentParseOutput(rt, filePath, len(data), mode, maxText, result), nil
}

func normalizeDocumentMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "auto", "snapshot", "summary":
		return "auto"
	case "text":
		return "text"
	case "tables", "table":
		return "tables"
	case "metadata", "meta":
		return "metadata"
	default:
		return "auto"
	}
}

func parseDocumentBytes(filePath string, data []byte, maxText, maxRows, maxSheets int) (documentParseResult, error) {
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".pdf":
		return parsePDFDocument(data, maxText), nil
	case ".docx":
		return parseDOCXDocument(data, maxText)
	case ".xlsx", ".xlsm":
		return parseXLSXDocument(data, maxRows, maxSheets, maxText)
	case ".csv":
		return parseDelimitedDocument(data, ',', maxRows, maxText, "CSV table"), nil
	case ".tsv":
		return parseDelimitedDocument(data, '\t', maxRows, maxText, "TSV table"), nil
	case ".rtf":
		return parseRTFDocument(data, maxText), nil
	case ".doc", ".xls":
		return parseLegacyOfficeDocument(data, ext, maxText), nil
	}

	if bytes.HasPrefix(data, []byte("%PDF-")) {
		return parsePDFDocument(data, maxText), nil
	}
	if isZipData(data) {
		if kind, ok := detectOOXMLKind(data); ok {
			switch kind {
			case "docx":
				return parseDOCXDocument(data, maxText)
			case "xlsx":
				return parseXLSXDocument(data, maxRows, maxSheets, maxText)
			}
		}
	}
	if isOLECompound(data) {
		return parseLegacyOfficeDocument(data, ext, maxText), nil
	}
	if looksText(data) {
		return documentParseResult{
			Format: "Plain text",
			Parser: "text fallback",
			Text:   truncateDocumentText(strings.ToValidUTF8(string(data), ""), maxText),
		}, nil
	}
	return parseLegacyOfficeDocument(data, ext, maxText), nil
}

func formatDocumentParseOutput(rt Runtime, filePath string, size int, mode string, maxText int, r documentParseResult) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s Document Parse\nPath: %s\nFormat: %s\nParser: %s\nSize: %d bytes\nMode: %s\n",
		rt.tag(), filePath, emptyDefault(r.Format, "unknown"), emptyDefault(r.Parser, "native"), size, mode))
	for _, w := range r.Warnings {
		b.WriteString("Warning: " + w + "\n")
	}

	if len(r.Meta) > 0 {
		b.WriteString("\nMetadata:\n")
		for _, m := range r.Meta {
			b.WriteString("  - " + m + "\n")
		}
	}
	if mode == "metadata" {
		return b.String()
	}

	if mode == "auto" || mode == "tables" {
		b.WriteString("\nTables:\n")
		if len(r.Tables) == 0 {
			b.WriteString("  (未检测到结构化表格)\n")
		} else {
			for _, t := range r.Tables {
				b.WriteString(t)
				if !strings.HasSuffix(t, "\n") {
					b.WriteString("\n")
				}
			}
		}
	}
	if mode == "tables" {
		return b.String()
	}

	if mode == "auto" || mode == "text" {
		b.WriteString("\nText:\n")
		text := strings.TrimSpace(truncateDocumentText(r.Text, maxText))
		if text == "" {
			text = "(未提取到正文文本)"
		}
		b.WriteString(text + "\n")
	}
	return b.String()
}

func parseDOCXDocument(data []byte, maxText int) (documentParseResult, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return documentParseResult{}, fmt.Errorf("DOCX ZIP 解析失败: %w", err)
	}
	files := zipFileMap(zr)
	var meta []string
	if f := files["docProps/core.xml"]; f != nil {
		if raw, err := readZipEntryLimited(f, maxOOXMLEntryRead); err == nil {
			meta = append(meta, parseCoreProps(raw)...)
		}
	}

	type part struct {
		label string
		name  string
	}
	parts := []part{{label: "正文", name: "word/document.xml"}}
	for _, prefix := range []string{"word/header", "word/footer"} {
		for _, name := range sortedZipNames(files, prefix, ".xml") {
			parts = append(parts, part{label: path.Base(name), name: name})
		}
	}
	for _, name := range []string{"word/footnotes.xml", "word/endnotes.xml", "word/comments.xml"} {
		if files[name] != nil {
			parts = append(parts, part{label: path.Base(name), name: name})
		}
	}

	var textParts []string
	paragraphs, tables := 0, 0
	for _, p := range parts {
		f := files[p.name]
		if f == nil {
			continue
		}
		raw, err := readZipEntryLimited(f, maxOOXMLEntryRead)
		if err != nil {
			continue
		}
		extracted, ps, ts := extractWordXMLText(raw)
		paragraphs += ps
		tables += ts
		if strings.TrimSpace(extracted) != "" {
			textParts = append(textParts, p.label+":\n"+extracted)
		}
	}

	meta = append(meta, fmt.Sprintf("paragraphs=%d", paragraphs), fmt.Sprintf("tables=%d", tables))
	return documentParseResult{
		Format: "Word DOCX",
		Parser: "OOXML ZIP/XML",
		Meta:   meta,
		Text:   truncateDocumentText(strings.Join(textParts, "\n\n"), maxText),
	}, nil
}

func parseXLSXDocument(data []byte, maxRows, maxSheets, maxText int) (documentParseResult, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return documentParseResult{}, fmt.Errorf("XLSX ZIP 解析失败: %w", err)
	}
	files := zipFileMap(zr)
	shared := []string{}
	if f := files["xl/sharedStrings.xml"]; f != nil {
		if raw, err := readZipEntryLimited(f, maxOOXMLEntryRead); err == nil {
			shared = parseSharedStrings(raw)
		}
	}
	sheets := parseWorkbookSheets(files)
	if len(sheets) == 0 {
		for _, name := range sortedZipNames(files, "xl/worksheets/sheet", ".xml") {
			sheets = append(sheets, xlsxSheetInfo{Name: path.Base(name), Path: name})
		}
	}

	var tables []string
	var text strings.Builder
	totalRows, totalCells := 0, 0
	shownSheets := 0
	for _, sheet := range sheets {
		if shownSheets >= maxSheets {
			break
		}
		f := files[sheet.Path]
		if f == nil {
			continue
		}
		raw, err := readZipEntryLimited(f, maxOOXMLEntryRead)
		if err != nil {
			continue
		}
		preview, rows, cells := parseWorksheetPreview(raw, shared, maxRows)
		totalRows += rows
		totalCells += cells
		if strings.TrimSpace(preview) != "" {
			tables = append(tables, fmt.Sprintf("  - Sheet %q (%s)\n%s", sheet.Name, sheet.Path, preview))
			text.WriteString("Sheet " + sheet.Name + "\n" + preview + "\n")
		}
		shownSheets++
	}
	warnings := []string{}
	if len(sheets) > shownSheets {
		warnings = append(warnings, fmt.Sprintf("工作表超过 %d 个，仅预览前 %d 个", maxSheets, maxSheets))
	}
	meta := []string{
		fmt.Sprintf("sheets=%d", len(sheets)),
		fmt.Sprintf("shared_strings=%d", len(shared)),
		fmt.Sprintf("rows_seen=%d", totalRows),
		fmt.Sprintf("cells_seen=%d", totalCells),
	}
	for _, s := range sheets[:min(len(sheets), maxSheets)] {
		meta = append(meta, "sheet="+s.Name)
	}
	return documentParseResult{
		Format:   "Excel XLSX/XLSM",
		Parser:   "OOXML ZIP/XML",
		Meta:     meta,
		Tables:   tables,
		Text:     truncateDocumentText(text.String(), maxText),
		Warnings: warnings,
	}, nil
}

func parseDelimitedDocument(data []byte, comma rune, maxRows, maxText int, format string) documentParseResult {
	r := csv.NewReader(bytes.NewReader(data))
	r.Comma = comma
	r.FieldsPerRecord = -1
	r.LazyQuotes = true

	var rows []string
	rowCount := 0
	maxCols := 0
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			rows = append(rows, "  ! parse error: "+err.Error())
			break
		}
		rowCount++
		if len(rec) > maxCols {
			maxCols = len(rec)
		}
		if len(rows) < maxRows {
			rows = append(rows, "  "+strings.Join(rec, "\t"))
		}
	}
	table := strings.Join(rows, "\n")
	return documentParseResult{
		Format: format,
		Parser: "encoding/csv",
		Meta:   []string{fmt.Sprintf("rows=%d", rowCount), fmt.Sprintf("max_columns=%d", maxCols)},
		Tables: []string{table + "\n"},
		Text:   truncateDocumentText(table, maxText),
	}
}

func parseRTFDocument(data []byte, maxText int) documentParseResult {
	return documentParseResult{
		Format: "RTF document",
		Parser: "native RTF text fallback",
		Text:   truncateDocumentText(stripRTFText(strings.ToValidUTF8(string(data), "")), maxText),
	}
}

func parseLegacyOfficeDocument(data []byte, ext string, maxText int) documentParseResult {
	format := "Binary document"
	switch ext {
	case ".doc":
		format = "Word DOC (OLE binary)"
	case ".xls":
		format = "Excel XLS (BIFF/OLE binary)"
	}
	return documentParseResult{
		Format: format,
		Parser: "native binary string fallback",
		Meta: []string{
			fmt.Sprintf("ole_compound=%v", isOLECompound(data)),
			"说明=老式 .doc/.xls 为二进制复合文档，本工具先提取可读字符串；复杂版式建议下载后用 Office/LibreOffice 复核",
		},
		Text:     truncateDocumentText(strings.Join(extractBinaryDocumentStrings(data, maxBinaryStringHits), "\n"), maxText),
		Warnings: []string{"binary fallback may miss cell boundaries, formulas, comments, and layout"},
	}
}

func parsePDFDocument(data []byte, maxText int) documentParseResult {
	pages := countPDFPages(data)
	meta := []string{fmt.Sprintf("pages~=%d", pages)}
	for _, key := range []string{"Title", "Author", "Subject", "Creator", "Producer"} {
		if v := extractPDFInfoString(data, key); v != "" {
			meta = append(meta, strings.ToLower(key)+"="+v)
		}
	}
	text, streams, warnings := extractPDFText(data, maxText)
	meta = append(meta, fmt.Sprintf("streams_scanned=%d", streams))
	return documentParseResult{
		Format:   "PDF document",
		Parser:   "native PDF text streams",
		Meta:     meta,
		Text:     truncateDocumentText(text, maxText),
		Warnings: warnings,
	}
}

func zipFileMap(zr *zip.Reader) map[string]*zip.File {
	files := make(map[string]*zip.File, len(zr.File))
	for _, f := range zr.File {
		files[path.Clean(strings.TrimPrefix(f.Name, "/"))] = f
	}
	return files
}

func sortedZipNames(files map[string]*zip.File, prefix, suffix string) []string {
	var names []string
	for name := range files {
		if strings.HasPrefix(name, prefix) && strings.HasSuffix(name, suffix) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func readZipEntryLimited(f *zip.File, max int) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	var b bytes.Buffer
	n, err := io.Copy(&b, io.LimitReader(rc, int64(max)+1))
	if err != nil {
		return nil, err
	}
	if n > int64(max) {
		return nil, fmt.Errorf("zip entry too large: %s", f.Name)
	}
	return b.Bytes(), nil
}

func isZipData(data []byte) bool {
	return len(data) >= 4 && data[0] == 'P' && data[1] == 'K'
}

func isOLECompound(data []byte) bool {
	return len(data) >= 8 &&
		data[0] == 0xd0 && data[1] == 0xcf && data[2] == 0x11 && data[3] == 0xe0 &&
		data[4] == 0xa1 && data[5] == 0xb1 && data[6] == 0x1a && data[7] == 0xe1
}

func detectOOXMLKind(data []byte) (string, bool) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", false
	}
	for _, f := range zr.File {
		name := path.Clean(f.Name)
		switch name {
		case "word/document.xml":
			return "docx", true
		case "xl/workbook.xml":
			return "xlsx", true
		}
	}
	return "", false
}

func parseCoreProps(data []byte) []string {
	want := map[string]bool{
		"title": true, "subject": true, "creator": true, "description": true,
		"created": true, "modified": true, "lastModifiedBy": true,
	}
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	var current string
	var b strings.Builder
	var out []string
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if want[t.Name.Local] {
				current = t.Name.Local
				b.Reset()
			}
		case xml.CharData:
			if current != "" {
				b.Write([]byte(t))
			}
		case xml.EndElement:
			if current != "" && t.Name.Local == current {
				if v := strings.TrimSpace(b.String()); v != "" {
					out = append(out, current+"="+v)
				}
				current = ""
			}
		}
	}
	return out
}

func extractWordXMLText(data []byte) (string, int, int) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	var b strings.Builder
	inText := false
	paragraphs, tables := 0, 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "t", "instrText":
				inText = true
			case "tab":
				b.WriteByte('\t')
			case "br", "cr":
				b.WriteByte('\n')
			case "p":
				paragraphs++
			case "tbl":
				tables++
			case "tc":
				if !endsWithWhitespace(b.String()) {
					b.WriteByte('\t')
				}
			}
		case xml.CharData:
			if inText {
				b.Write([]byte(t))
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t", "instrText":
				inText = false
			case "p", "tr":
				trimBuilderRight(&b, "\t ")
				if !strings.HasSuffix(b.String(), "\n") {
					b.WriteByte('\n')
				}
			}
		}
	}
	return cleanDocumentText(b.String()), paragraphs, tables
}

type xlsxSheetInfo struct {
	Name string
	Path string
}

func parseWorkbookSheets(files map[string]*zip.File) []xlsxSheetInfo {
	wb := files["xl/workbook.xml"]
	if wb == nil {
		return nil
	}
	raw, err := readZipEntryLimited(wb, maxOOXMLEntryRead)
	if err != nil {
		return nil
	}
	rels := parseWorkbookRels(files)
	dec := xml.NewDecoder(bytes.NewReader(raw))
	dec.Strict = false
	var sheets []xlsxSheetInfo
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "sheet" {
			continue
		}
		name, rid := "", ""
		for _, a := range se.Attr {
			switch a.Name.Local {
			case "name":
				name = a.Value
			case "id":
				rid = a.Value
			}
		}
		target := rels[rid]
		if target == "" {
			target = fmt.Sprintf("xl/worksheets/sheet%d.xml", len(sheets)+1)
		}
		if !strings.HasPrefix(target, "xl/") {
			target = path.Clean("xl/" + strings.TrimPrefix(target, "/"))
		}
		if name == "" {
			name = path.Base(target)
		}
		sheets = append(sheets, xlsxSheetInfo{Name: name, Path: target})
	}
	return sheets
}

func parseWorkbookRels(files map[string]*zip.File) map[string]string {
	out := map[string]string{}
	f := files["xl/_rels/workbook.xml.rels"]
	if f == nil {
		return out
	}
	raw, err := readZipEntryLimited(f, maxOOXMLEntryRead)
	if err != nil {
		return out
	}
	dec := xml.NewDecoder(bytes.NewReader(raw))
	dec.Strict = false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "Relationship" {
			continue
		}
		id, target := "", ""
		for _, a := range se.Attr {
			switch a.Name.Local {
			case "Id":
				id = a.Value
			case "Target":
				target = a.Value
			}
		}
		if id != "" && target != "" {
			target = strings.TrimPrefix(target, "/")
			if strings.HasPrefix(target, "xl/") {
				out[id] = path.Clean(target)
			} else {
				out[id] = path.Clean("xl/" + target)
			}
		}
	}
	return out
}

func parseSharedStrings(data []byte) []string {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	var out []string
	var b strings.Builder
	inSI, inText := false, false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "si":
				inSI = true
				b.Reset()
			case "t":
				if inSI {
					inText = true
				}
			}
		case xml.CharData:
			if inText {
				b.Write([]byte(t))
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inText = false
			case "si":
				out = append(out, b.String())
				inSI = false
			}
		}
	}
	return out
}

func parseWorksheetPreview(data []byte, shared []string, maxRows int) (string, int, int) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	rows := map[int]map[int]string{}
	inCell, inV, inT, inF := false, false, false, false
	cellRef, cellType := "", ""
	var value, formula strings.Builder
	rowCount, cellCount := 0, 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "c":
				inCell = true
				cellRef, cellType = "", ""
				value.Reset()
				formula.Reset()
				for _, a := range t.Attr {
					switch a.Name.Local {
					case "r":
						cellRef = a.Value
					case "t":
						cellType = a.Value
					}
				}
			case "v":
				if inCell {
					inV = true
				}
			case "t":
				if inCell {
					inT = true
				}
			case "f":
				if inCell {
					inF = true
				}
			}
		case xml.CharData:
			if inV || inT {
				value.Write([]byte(t))
			}
			if inF {
				formula.Write([]byte(t))
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "v":
				inV = false
			case "t":
				inT = false
			case "f":
				inF = false
			case "c":
				row, col := cellRefToRowCol(cellRef)
				v := normalizeXLSXCell(cellType, strings.TrimSpace(value.String()), strings.TrimSpace(formula.String()), shared)
				if row > 0 && col > 0 && v != "" {
					if _, ok := rows[row]; !ok {
						rows[row] = map[int]string{}
						rowCount++
					}
					rows[row][col] = v
					cellCount++
				}
				inCell = false
			}
		}
	}
	rowNums := make([]int, 0, len(rows))
	for r := range rows {
		rowNums = append(rowNums, r)
	}
	sort.Ints(rowNums)
	var b strings.Builder
	shown := 0
	for _, r := range rowNums {
		if shown >= maxRows {
			b.WriteString(fmt.Sprintf("    ...(rows 超过 %d，仅预览前 %d 行)...\n", maxRows, maxRows))
			break
		}
		cols := make([]int, 0, len(rows[r]))
		for c := range rows[r] {
			cols = append(cols, c)
		}
		sort.Ints(cols)
		var cells []string
		for i, c := range cols {
			if i >= maxXLSXColsPreview {
				cells = append(cells, "...")
				break
			}
			cells = append(cells, fmt.Sprintf("%s=%s", colName(c), truncateOneLine(rows[r][c], 160)))
		}
		b.WriteString(fmt.Sprintf("    Row %d: %s\n", r, strings.Join(cells, " | ")))
		shown++
	}
	return b.String(), rowCount, cellCount
}

func normalizeXLSXCell(cellType, raw, formula string, shared []string) string {
	val := raw
	switch cellType {
	case "s":
		if idx, err := strconv.Atoi(raw); err == nil && idx >= 0 && idx < len(shared) {
			val = shared[idx]
		}
	case "b":
		if raw == "1" {
			val = "TRUE"
		} else if raw == "0" {
			val = "FALSE"
		}
	}
	if formula != "" {
		if val != "" {
			return "=" + formula + " => " + val
		}
		return "=" + formula
	}
	return val
}

func cellRefToRowCol(ref string) (int, int) {
	col := 0
	i := 0
	for ; i < len(ref); i++ {
		ch := ref[i]
		if ch < 'A' || ch > 'Z' {
			break
		}
		col = col*26 + int(ch-'A'+1)
	}
	row, _ := strconv.Atoi(ref[i:])
	return row, col
}

func colName(col int) string {
	if col <= 0 {
		return "?"
	}
	var out []byte
	for col > 0 {
		col--
		out = append([]byte{byte('A' + col%26)}, out...)
		col /= 26
	}
	return string(out)
}

func countPDFPages(data []byte) int {
	re := regexp.MustCompile(`/Type\s*/Page\b`)
	return len(re.FindAll(data, -1))
}

func extractPDFInfoString(data []byte, key string) string {
	re := regexp.MustCompile(`/` + regexp.QuoteMeta(key) + `\s*(\((?:\\.|[^\\)])*\)|<[^>]{2,512}>)`)
	m := re.FindSubmatch(data[:min(len(data), 256*1024)])
	if len(m) < 2 {
		return ""
	}
	return decodePDFStringToken(string(m[1]))
}

func extractPDFText(data []byte, maxText int) (string, int, []string) {
	var chunks []string
	warnings := []string{}
	streams := 0
	for off := 0; off < len(data); {
		idx := bytes.Index(data[off:], []byte("stream"))
		if idx < 0 {
			break
		}
		streamMarker := off + idx
		start := streamMarker + len("stream")
		if start < len(data) && data[start] == '\r' {
			start++
		}
		if start < len(data) && data[start] == '\n' {
			start++
		}
		endRel := bytes.Index(data[start:], []byte("endstream"))
		if endRel < 0 {
			break
		}
		end := start + endRel
		raw := data[start:end]
		if len(raw) > maxPDFStreamRead {
			raw = raw[:maxPDFStreamRead]
			warnings = append(warnings, "PDF stream exceeded per-stream cap and was truncated")
		}
		dictStart := max(0, streamMarker-2048)
		dict := data[dictStart:streamMarker]
		decoded := raw
		if bytes.Contains(dict, []byte("/FlateDecode")) {
			if z, err := zlib.NewReader(bytes.NewReader(raw)); err == nil {
				if out, err := io.ReadAll(io.LimitReader(z, maxPDFStreamRead)); err == nil {
					decoded = out
				}
				_ = z.Close()
			}
		}
		if t := parsePDFContentText(decoded); t != "" {
			chunks = append(chunks, t)
		}
		streams++
		if len([]rune(strings.Join(chunks, "\n"))) >= maxText {
			break
		}
		off = end + len("endstream")
	}
	if len(chunks) == 0 {
		if t := parsePDFContentText(data[:min(len(data), 512*1024)]); t != "" {
			chunks = append(chunks, t)
		}
	}
	return cleanDocumentText(strings.Join(chunks, "\n")), streams, dedupeStrings(warnings)
}

func parsePDFContentText(data []byte) string {
	if !bytes.Contains(data, []byte("Tj")) && !bytes.Contains(data, []byte("TJ")) &&
		!bytes.Contains(data, []byte("'")) && !bytes.Contains(data, []byte("\"")) {
		return ""
	}
	var out []string
	for i := 0; i < len(data); i++ {
		switch data[i] {
		case '(':
			s, next := readPDFLiteralString(data, i)
			if next > i {
				if decoded := strings.TrimSpace(s); decoded != "" {
					out = append(out, decoded)
				}
				i = next - 1
			}
		case '<':
			if i+1 < len(data) && data[i+1] == '<' {
				continue
			}
			s, next := readPDFHexString(data, i)
			if next > i {
				if decoded := strings.TrimSpace(s); decoded != "" {
					out = append(out, decoded)
				}
				i = next - 1
			}
		}
	}
	return strings.Join(out, " ")
}

func readPDFLiteralString(data []byte, start int) (string, int) {
	var b strings.Builder
	depth := 0
	for i := start; i < len(data); i++ {
		ch := data[i]
		if i == start {
			depth = 1
			continue
		}
		if ch == '\\' && i+1 < len(data) {
			i++
			next := data[i]
			switch next {
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			case 't':
				b.WriteByte('\t')
			case 'b', 'f':
			case '(', ')', '\\':
				b.WriteByte(next)
			default:
				if next >= '0' && next <= '7' {
					oct := []byte{next}
					for j := 0; j < 2 && i+1 < len(data) && data[i+1] >= '0' && data[i+1] <= '7'; j++ {
						i++
						oct = append(oct, data[i])
					}
					if v, err := strconv.ParseInt(string(oct), 8, 32); err == nil {
						b.WriteRune(rune(v))
					}
				} else {
					b.WriteByte(next)
				}
			}
			continue
		}
		switch ch {
		case '(':
			depth++
			b.WriteByte(ch)
		case ')':
			depth--
			if depth == 0 {
				return strings.ToValidUTF8(b.String(), ""), i + 1
			}
			b.WriteByte(ch)
		default:
			b.WriteByte(ch)
		}
	}
	return "", start
}

func readPDFHexString(data []byte, start int) (string, int) {
	end := bytes.IndexByte(data[start+1:], '>')
	if end < 0 {
		return "", start
	}
	raw := strings.Map(func(r rune) rune {
		if r == ' ' || r == '\n' || r == '\r' || r == '\t' {
			return -1
		}
		return r
	}, string(data[start+1:start+1+end]))
	if len(raw)%2 == 1 {
		raw += "0"
	}
	buf := make([]byte, len(raw)/2)
	for i := 0; i < len(buf); i++ {
		v, err := strconv.ParseUint(raw[i*2:i*2+2], 16, 8)
		if err != nil {
			return "", start + 1 + end + 1
		}
		buf[i] = byte(v)
	}
	return decodePDFBytes(buf), start + 1 + end + 1
}

func decodePDFStringToken(token string) string {
	if strings.HasPrefix(token, "(") {
		s, _ := readPDFLiteralString([]byte(token), 0)
		return s
	}
	if strings.HasPrefix(token, "<") {
		s, _ := readPDFHexString([]byte(token), 0)
		return s
	}
	return token
}

func decodePDFBytes(buf []byte) string {
	if len(buf) >= 2 && buf[0] == 0xfe && buf[1] == 0xff {
		var units []uint16
		for i := 2; i+1 < len(buf); i += 2 {
			units = append(units, uint16(buf[i])<<8|uint16(buf[i+1]))
		}
		return string(utf16.Decode(units))
	}
	return strings.ToValidUTF8(string(buf), "")
}

func extractBinaryDocumentStrings(data []byte, limit int) []string {
	out := extractStrings(data, 4)
	out = append(out, extractUTF16LEDocumentStrings(data, 4)...)
	out = dedupeStrings(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func extractUTF16LEDocumentStrings(data []byte, minLen int) []string {
	var out []string
	var units []uint16
	flush := func() {
		if len(units) >= minLen {
			s := strings.TrimSpace(string(utf16.Decode(units)))
			if s != "" {
				out = append(out, s)
			}
		}
		units = nil
	}
	for i := 0; i+1 < len(data); i += 2 {
		u := uint16(data[i]) | uint16(data[i+1])<<8
		if u == 9 || u == 10 || u == 13 || (u >= 32 && u <= 0xd7ff) || (u >= 0xe000 && u <= 0xfffd) {
			units = append(units, u)
		} else {
			flush()
		}
	}
	flush()
	return out
}

func stripRTFText(s string) string {
	s = regexp.MustCompile(`\\'[0-9a-fA-F]{2}`).ReplaceAllStringFunc(s, func(m string) string {
		v, _ := strconv.ParseUint(m[2:], 16, 8)
		return string(byte(v))
	})
	s = regexp.MustCompile(`\\[a-zA-Z]+-?\d* ?`).ReplaceAllString(s, " ")
	s = strings.NewReplacer("{", " ", "}", " ", "\\", " ").Replace(s)
	return cleanDocumentText(s)
}

func cleanDocumentText(s string) string {
	s = strings.ToValidUTF8(s, "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	blank := false
	for _, line := range lines {
		line = strings.TrimSpace(strings.Join(strings.Fields(line), " "))
		if line == "" {
			if !blank {
				out = append(out, "")
			}
			blank = true
			continue
		}
		out = append(out, line)
		blank = false
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func truncateDocumentText(s string, maxChars int) string {
	if maxChars <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= maxChars {
		return s
	}
	return string(r[:maxChars]) + "\n...(输出已截断)..."
}

func trimBuilderRight(b *strings.Builder, cutset string) {
	s := strings.TrimRight(b.String(), cutset)
	b.Reset()
	b.WriteString(s)
}

func endsWithWhitespace(s string) bool {
	if s == "" {
		return true
	}
	last := s[len(s)-1]
	return last == ' ' || last == '\t' || last == '\n'
}
