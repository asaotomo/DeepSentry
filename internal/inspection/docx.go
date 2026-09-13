package inspection

import (
	"archive/zip"
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"strings"
	"time"
)

const (
	docxNavy    = "0E2344"
	docxTeal    = "1F6F6A"
	docxGold    = "C5A46E"
	docxInk     = "2C2C2C"
	docxMuted   = "6B6258"
	docxLine    = "D8D0C4"
	docxPaper   = "F7F5F0"
	docxQuote   = "F3F0E8"
	docxWhite   = "FFFFFF"
	docxPass    = "1F6B4A"
	docxPassBg  = "E5F3EB"
	docxAlert   = "9A5B12"
	docxAlertBg = "F8EEDD"
	docxFail    = "9B2C2C"
	docxFailBg  = "F8E4E4"
	docxWatch   = "5B5348"
	docxWatchBg = "EEEAE2"
	docxPageW   = 11906
	docxPageH   = 16838
	docxWidth   = 9026
	coverTopH   = 5200
	coverMidH   = 6800
	coverBotH   = 4838
	docxEMU     = 635
)

type docxDoc struct {
	body      strings.Builder
	media     map[string][]byte
	pictureID int
	figureID  int
	hasCover  bool
}

func newDocx() *docxDoc {
	return &docxDoc{media: map[string][]byte{}}
}

func rFonts() string { return bodyFonts() }

func bodyFonts() string {
	return `<w:rFonts w:ascii="Calibri" w:hAnsi="Calibri" w:eastAsia="微软雅黑" w:cs="Calibri"/>`
}

func headingFonts() string {
	return `<w:rFonts w:ascii="Calibri Light" w:hAnsi="Calibri Light" w:eastAsia="黑体" w:cs="Calibri Light"/>`
}

func uiFonts() string {
	return `<w:rFonts w:ascii="Calibri" w:hAnsi="Calibri" w:eastAsia="微软雅黑" w:cs="Calibri"/>`
}

func coverBrandFonts() string {
	return `<w:rFonts w:ascii="Calibri Light" w:hAnsi="Calibri Light" w:eastAsia="微软雅黑" w:cs="Calibri Light"/>`
}

func coverTitleFonts() string {
	return `<w:rFonts w:ascii="Calibri" w:hAnsi="Calibri" w:eastAsia="华文中宋" w:cs="Calibri"/>`
}

func statusLabel(status string) string {
	switch status {
	case "pass":
		return "正常"
	case "alert":
		return "告警"
	case "failed":
		return "失败"
	case "observed":
		return "待复核"
	default:
		return status
	}
}

func statusTheme(status string) (fg, bg string) {
	switch status {
	case "pass":
		return docxPass, docxPassBg
	case "alert":
		return docxAlert, docxAlertBg
	case "failed":
		return docxFail, docxFailBg
	default:
		return docxWatch, docxWatchBg
	}
}

func (d *docxDoc) cover(title, subtitle string, started, finished time.Time) {
	d.hasCover = true
	d.body.WriteString(spacer(900))
	d.body.WriteString(styledP("", 20, docxMuted, false, "DEEPSENTRY  /  INSPECTION REPORT"))
	d.body.WriteString(spacer(500))
	d.body.WriteString(styledP("Title", 52, "000000", true, "安全设备巡检报告"))
	d.body.WriteString(spacer(200))
	if title != "" {
		d.richPara(title)
	}
	if subtitle != "" {
		d.richPara(subtitle)
	}
	d.body.WriteString(spacer(600))
	d.table([][]string{{"报告日期", reportTime(started)}, {"完成时间", reportTime(finished)}, {"生成工具", "DeepSentry"}}, false)
	d.body.WriteString(spacer(240))
	d.richPara("本报告汇总设备运行状态、安全告警和采集证据，供运维人员定位异常、安排处置和复核。具体结论见巡检概览。")
	d.body.WriteString(`<w:p><w:pPr><w:sectPr><w:type w:val="nextPage"/><w:pgSz w:w="11906" w:h="16838"/><w:pgMar w:top="1330" w:right="1440" w:bottom="1330" w:left="1440"/></w:sectPr></w:pPr></w:p>`)
}

func reportTime(t time.Time) string {
	if t.IsZero() {
		return "未记录"
	}
	return t.Format("2006-01-02 15:04:05 -0700")
}

func (d *docxDoc) nextCoverImage() int {
	d.pictureID++
	d.media[fmt.Sprintf("word/media/image%d.png", d.pictureID)] = navyPNG()
	return d.pictureID
}

func navyPNG() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	fill := color.RGBA{0x0E, 0x23, 0x44, 0xFF}
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			img.SetRGBA(x, y, fill)
		}
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}

func coverBackground(id int) string {
	cx := itoa(docxPageW * docxEMU)
	cy := itoa(docxPageH * docxEMU)
	rid := fmt.Sprintf("rIdImg%d", id)
	return `<w:p><w:pPr><w:spacing w:before="0" w:after="0"/></w:pPr><w:r><w:drawing><wp:anchor distT="0" distB="0" distL="0" distR="0" simplePos="0" relativeHeight="0" behindDoc="1" locked="1" layoutInCell="0" allowOverlap="1"><wp:simplePos x="0" y="0"/><wp:positionH relativeFrom="page"><wp:posOffset>0</wp:posOffset></wp:positionH><wp:positionV relativeFrom="page"><wp:posOffset>0</wp:posOffset></wp:positionV><wp:extent cx="` + cx + `" cy="` + cy + `"/><wp:effectExtent l="0" t="0" r="0" b="0"/><wp:wrapNone/><wp:docPr id="` + itoa(id) + `" name="CoverFill"/><wp:cNvGraphicFramePr/><a:graphic><a:graphicData uri="http://schemas.openxmlformats.org/drawingml/2006/picture"><pic:pic><pic:nvPicPr><pic:cNvPr id="` + itoa(id) + `" name="CoverFill"/><pic:cNvPicPr/></pic:nvPicPr><pic:blipFill><a:blip r:embed="` + rid + `"/><a:stretch><a:fillRect/></a:stretch></pic:blipFill><pic:spPr><a:xfrm><a:off x="0" y="0"/><a:ext cx="` + cx + `" cy="` + cy + `"/></a:xfrm><a:prstGeom prst="rect"><a:avLst/></a:prstGeom></pic:spPr></pic:pic></a:graphicData></a:graphic></wp:anchor></w:drawing></w:r></w:p>`
}

func coverTblPr() string {
	return `<w:tblW w:w="` + itoa(docxPageW) + `" w:type="dxa"/><w:jc w:val="center"/><w:tblInd w:w="0" w:type="dxa"/><w:tblLayout w:type="fixed"/><w:tblCellMar><w:top w:w="0"/><w:left w:w="0"/><w:bottom w:w="0"/><w:right w:w="0"/></w:tblCellMar><w:tblBorders>` + tblBorder(docxNavy, 0) + `</w:tblBorders>`
}

func coverBand(height int, fill string, topPad, leftPad int, lines []string) string {
	var b strings.Builder
	b.WriteString(`<w:tr><w:trPr><w:trHeight w:val="` + itoa(height) + `" w:hRule="exact"/></w:trPr><w:tc><w:tcPr><w:tcW w:w="` + itoa(docxPageW) + `" w:type="dxa"/><w:shd w:val="clear" w:fill="` + fill + `"/><w:tcMar><w:top w:w="` + itoa(topPad) + `"/><w:left w:w="` + itoa(leftPad) + `"/><w:bottom w:w="200"/><w:right w:w="` + itoa(leftPad) + `"/></w:tcMar></w:tcPr>`)
	for _, line := range lines {
		b.WriteString(line)
	}
	b.WriteString(`</w:tc></w:tr>`)
	return b.String()
}

func coverLine(fonts string, sz int, color string, bold bool, after int, text string) string {
	rpr := fonts + fmt.Sprintf(`<w:sz w:val="%d"/><w:szCs w:val="%d"/><w:color w:val="%s"/>`, sz, sz, color)
	if bold {
		rpr += `<w:b/>`
	}
	return `<w:p><w:pPr><w:jc w:val="center"/><w:spacing w:before="0" w:after="` + itoa(after) + `"/></w:pPr><w:r><w:rPr>` + rpr + `</w:rPr><w:t xml:space="preserve">` + xmlText(text) + `</w:t></w:r></w:p>`
}

func coverRule() string {
	return `<w:p><w:pPr><w:jc w:val="center"/><w:pBdr><w:bottom w:val="single" w:sz="12" w:space="1" w:color="` + docxGold + `"/></w:pBdr><w:spacing w:before="120" w:after="280"/><w:ind w:left="2200" w:right="2200"/></w:pPr></w:p>`
}

func coverSectPr() string {
	return `<w:pgSz w:w="` + itoa(docxPageW) + `" w:h="` + itoa(docxPageH) + `"/><w:pgMar w:top="0" w:right="0" w:bottom="0" w:left="0" w:header="0" w:footer="0"/>`
}

func (d *docxDoc) heading(level int, text string) {
	style := fmt.Sprintf("Heading%d", level)
	d.body.WriteString(`<w:p><w:pPr><w:pStyle w:val="` + style + `"/></w:pPr><w:r><w:rPr>` + headingFonts() + `</w:rPr><w:t xml:space="preserve">` + xmlText(text) + `</w:t></w:r></w:p>`)
}

func (d *docxDoc) para(text string) {
	d.richPara(text)
}

func (d *docxDoc) richPara(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	d.body.WriteString(`<w:p><w:pPr><w:pStyle w:val="Normal"/></w:pPr>`)
	d.body.WriteString(richRuns(text, docxInk, 21, false))
	d.body.WriteString(`</w:p>`)
}

func (d *docxDoc) caption(text string) {
	d.body.WriteString(`<w:p><w:pPr><w:pStyle w:val="Caption"/></w:pPr><w:r><w:rPr>` + uiFonts() + `<w:i/><w:sz w:val="18"/><w:color w:val="` + docxMuted + `"/></w:rPr><w:t xml:space="preserve">` + xmlText(text) + `</w:t></w:r></w:p>`)
}

func (d *docxDoc) statusLine(status string) {
	fg, bg := statusTheme(status)
	label := statusLabel(status)
	d.body.WriteString(`<w:p><w:pPr><w:spacing w:after="160"/></w:pPr><w:r><w:rPr>` + uiFonts() + `<w:b/><w:sz w:val="20"/><w:color w:val="` + fg + `"/><w:shd w:val="clear" w:fill="` + bg + `"/></w:rPr><w:t xml:space="preserve">  ` + xmlText(label) + `  </w:t></w:r></w:p>`)
}

func (d *docxDoc) quote(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if utf8Len(text) > 4000 {
		text = string([]rune(text)[:4000]) + "\n[报告摘要截断，完整内容见文本证据]"
	}
	d.body.WriteString(`<w:tbl><w:tblPr><w:tblW w:w="` + itoa(docxWidth) + `" w:type="dxa"/><w:tblBorders>` + tblBorder(docxTeal, 8) + `</w:tblBorders></w:tblPr><w:tblGrid><w:gridCol w:w="` + itoa(docxWidth) + `"/></w:tblGrid><w:tr><w:tc><w:tcPr><w:tcW w:w="` + itoa(docxWidth) + `" w:type="dxa"/><w:shd w:val="clear" w:fill="` + docxQuote + `"/><w:tcMar><w:top w:w="80"/><w:left w:w="140"/><w:bottom w:w="80"/><w:right w:w="140"/></w:tcMar></w:tcPr>`)
	for _, line := range strings.Split(text, "\n") {
		d.body.WriteString(`<w:p><w:pPr><w:spacing w:after="40" w:line="276" w:lineRule="auto"/></w:pPr><w:r><w:rPr>` + uiFonts() + `<w:rFonts w:ascii="Consolas" w:hAnsi="Consolas"/><w:sz w:val="18"/><w:color w:val="3F3A33"/></w:rPr><w:t xml:space="preserve">` + xmlText(line) + `</w:t></w:r></w:p>`)
	}
	d.body.WriteString(`</w:tc></w:tr></w:tbl>`)
	d.body.WriteString(spacer(120))
}

func (d *docxDoc) bullets(items []string) {
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		d.body.WriteString(`<w:p><w:pPr><w:pStyle w:val="ListBullet"/><w:numPr><w:ilvl w:val="0"/><w:numId w:val="1"/></w:numPr></w:pPr>`)
		d.body.WriteString(richRuns(item, docxInk, 21, false))
		d.body.WriteString(`</w:p>`)
	}
}

func (d *docxDoc) table(rows [][]string, header bool) {
	if len(rows) == 0 {
		return
	}
	cols := 0
	for _, row := range rows {
		if len(row) > cols {
			cols = len(row)
		}
	}
	if cols == 0 {
		return
	}
	widths := colWidths(cols)
	d.body.WriteString(`<w:tbl><w:tblPr><w:tblW w:w="` + itoa(docxWidth) + `" w:type="dxa"/><w:tblBorders>` + tblBorder(docxLine, 4) + `</w:tblBorders><w:tblLayout w:type="fixed"/><w:tblLook w:firstRow="1" w:noHBand="0" w:noVBand="1"/></w:tblPr><w:tblGrid>`)
	for _, w := range widths {
		d.body.WriteString(`<w:gridCol w:w="` + itoa(w) + `"/>`)
	}
	d.body.WriteString(`</w:tblGrid>`)
	for i, row := range rows {
		isHead := header && i == 0
		d.body.WriteString(`<w:tr><w:trPr><w:cantSplit/>`)
		if isHead {
			d.body.WriteString(`<w:tblHeader/>`)
		}
		d.body.WriteString(`</w:trPr>`)
		for c := 0; c < cols; c++ {
			cell := ""
			if c < len(row) {
				cell = row[c]
			}
			fill, color, bold := docxWhite, docxInk, false
			if isHead {
				fill, color, bold = docxNavy, docxWhite, true
			} else if i%2 == 0 {
				fill = docxPaper
			}
			if !isHead && c < len(row) {
				if fg, bg, ok := statusCellTheme(cell); ok {
					fill, color, bold = bg, fg, true
				}
			}
			d.body.WriteString(`<w:tc><w:tcPr><w:tcW w:w="` + itoa(widths[c]) + `" w:type="dxa"/><w:shd w:val="clear" w:fill="` + fill + `"/><w:vAlign w:val="center"/><w:tcMar><w:top w:w="80"/><w:left w:w="100"/><w:bottom w:w="80"/><w:right w:w="100"/></w:tcMar></w:tcPr><w:p><w:pPr><w:ind w:firstLine="0"/><w:spacing w:after="0" w:line="260" w:lineRule="auto"/></w:pPr><w:r><w:rPr>` + uiFonts() + `<w:sz w:val="18"/>`)
			if bold {
				d.body.WriteString(`<w:b/>`)
			}
			d.body.WriteString(`<w:color w:val="` + color + `"/></w:rPr><w:t xml:space="preserve">` + xmlText(cell) + `</w:t></w:r></w:p></w:tc>`)
		}
		d.body.WriteString(`</w:tr>`)
	}
	d.body.WriteString(`</w:tbl>`)
	d.body.WriteString(spacer(160))
}

func statusCellTheme(cell string) (string, string, bool) {
	switch strings.TrimSpace(cell) {
	case "正常", "pass":
		fg, bg := statusTheme("pass")
		return fg, bg, true
	case "告警", "alert":
		fg, bg := statusTheme("alert")
		return fg, bg, true
	case "失败", "failed":
		fg, bg := statusTheme("failed")
		return fg, bg, true
	case "待复核", "observed":
		fg, bg := statusTheme("observed")
		return fg, bg, true
	}
	return "", "", false
}

func (d *docxDoc) image(data []byte, format string, size image.Config, caption string) {
	if len(data) == 0 || size.Width <= 0 || size.Height <= 0 {
		return
	}
	d.pictureID++
	d.figureID++
	id := fmt.Sprintf("rIdImg%d", d.pictureID)
	ext := "png"
	if format == "jpeg" {
		ext = "jpg"
	}
	name := fmt.Sprintf("image%d.%s", d.pictureID, ext)
	d.media["word/media/"+name] = data
	width := min(int64(5486400), int64(size.Width)*9525)
	height := width * int64(size.Height) / int64(size.Width)
	if height > 6200000 {
		width = width * 6200000 / height
		height = 6200000
	}
	d.body.WriteString(fmt.Sprintf(`<w:p><w:pPr><w:keepNext/><w:ind w:firstLine="0"/><w:jc w:val="center"/><w:spacing w:before="120" w:after="40"/></w:pPr><w:r><w:drawing><wp:inline><wp:extent cx="%d" cy="%d"/><wp:docPr id="%d" name="Figure %d"/><a:graphic><a:graphicData uri="http://schemas.openxmlformats.org/drawingml/2006/picture"><pic:pic><pic:nvPicPr><pic:cNvPr id="%d" name="%s"/><pic:cNvPicPr/></pic:nvPicPr><pic:blipFill><a:blip r:embed="%s"/><a:stretch><a:fillRect/></a:stretch></pic:blipFill><pic:spPr><a:xfrm><a:off x="0" y="0"/><a:ext cx="%d" cy="%d"/></a:xfrm><a:prstGeom prst="rect"><a:avLst/></a:prstGeom></pic:spPr></pic:pic></a:graphicData></a:graphic></wp:inline></w:drawing></w:r></w:p>`, width, height, d.pictureID, d.figureID, d.pictureID, name, id, width, height))
	if caption == "" {
		caption = fmt.Sprintf("图 %d  截图证据", d.figureID)
	} else if !strings.HasPrefix(caption, "图 ") {
		caption = fmt.Sprintf("图 %d  %s", d.figureID, caption)
	}
	d.caption(caption)
}

func (d *docxDoc) save(path string) error {
	rels := `<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rIdStyles" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/><Relationship Id="rIdNumbering" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/numbering" Target="numbering.xml"/><Relationship Id="rIdFonts" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/fontTable" Target="fontTable.xml"/><Relationship Id="rIdHeader" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/header" Target="header1.xml"/><Relationship Id="rIdFooter" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/footer" Target="footer1.xml"/>`
	for i := 1; i <= d.pictureID; i++ {
		ext := "png"
		name := fmt.Sprintf("image%d.png", i)
		if _, ok := d.media["word/media/"+fmt.Sprintf("image%d.jpg", i)]; ok {
			ext = "jpg"
			name = fmt.Sprintf("image%d.jpg", i)
		}
		rels += fmt.Sprintf(`<Relationship Id="rIdImg%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="media/%s"/>`, i, name)
		_ = ext
	}
	rels += `</Relationships>`
	docXML := `<?xml version="1.0" encoding="UTF-8"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:wp="http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:pic="http://schemas.openxmlformats.org/drawingml/2006/picture"><w:body>` + d.body.String() + `<w:sectPr><w:headerReference w:type="default" r:id="rIdHeader"/><w:footerReference w:type="default" r:id="rIdFooter"/><w:pgSz w:w="` + itoa(docxPageW) + `" w:h="` + itoa(docxPageH) + `"/><w:pgMar w:top="1330" w:right="1440" w:bottom="1330" w:left="1440" w:header="720" w:footer="720"/><w:pgNumType w:start="1"/></w:sectPr></w:body></w:document>`
	parts := map[string]string{
		"[Content_Types].xml":          docxContentTypes(),
		"_rels/.rels":                  `<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/></Relationships>`,
		"word/document.xml":            docXML,
		"word/_rels/document.xml.rels": rels,
		"word/styles.xml":              docxStyles(),
		"word/numbering.xml":           docxNumbering(),
		"word/fontTable.xml":           docxFontTable(),
		"word/header1.xml":             docxHeader(),
		"word/footer1.xml":             docxFooter(),
	}
	return writeDocxParts(path, parts, d.media)
}

func writeDocxParts(path string, parts map[string]string, media map[string][]byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	z := zip.NewWriter(file)
	closed := false
	defer func() {
		if !closed {
			z.Close()
			file.Close()
		}
	}()
	add := func(name string, b []byte) error {
		w, e := z.Create(name)
		if e != nil {
			return e
		}
		_, e = w.Write(b)
		return e
	}
	for name, content := range parts {
		if err = add(name, []byte(content)); err != nil {
			return err
		}
	}
	for name, content := range media {
		if err = add(name, content); err != nil {
			return err
		}
	}
	if err = z.Close(); err != nil {
		return err
	}
	err = file.Close()
	closed = true
	return err
}

func styledP(style string, sz int, color string, bold bool, text string) string {
	ppr := `<w:spacing w:after="60"/>`
	if style != "" {
		ppr = `<w:pStyle w:val="` + style + `"/>` + ppr
	}
	rpr := rFonts() + fmt.Sprintf(`<w:sz w:val="%d"/><w:szCs w:val="%d"/><w:color w:val="%s"/>`, sz, sz, color)
	if bold {
		rpr += `<w:b/>`
	}
	return `<w:p><w:pPr>` + ppr + `</w:pPr><w:r><w:rPr>` + rpr + `</w:rPr><w:t xml:space="preserve">` + xmlText(text) + `</w:t></w:r></w:p>`
}

func spacer(twips int) string {
	return fmt.Sprintf(`<w:p><w:pPr><w:spacing w:before="%d" w:after="0"/></w:pPr></w:p>`, twips)
}

func tblBorder(color string, sz int) string {
	if sz <= 0 {
		return `<w:top w:val="nil"/><w:left w:val="nil"/><w:bottom w:val="nil"/><w:right w:val="nil"/><w:insideH w:val="nil"/><w:insideV w:val="nil"/>`
	}
	edge := fmt.Sprintf(`w:val="single" w:sz="%d" w:space="0" w:color="%s"`, sz, color)
	return `<w:top ` + edge + `/><w:left ` + edge + `/><w:bottom ` + edge + `/><w:right ` + edge + `/><w:insideH ` + edge + `/><w:insideV ` + edge + `/>`
}

func colWidths(n int) []int {
	if n == 5 {
		return []int{600, 2100, 2800, 1000, 2526}
	}
	if n == 3 {
		return []int{1400, 1000, 6626}
	}
	if n == 2 {
		return []int{2200, 6826}
	}
	if n <= 0 {
		return nil
	}
	base := docxWidth / n
	out := make([]int, n)
	sum := 0
	for i := 0; i < n; i++ {
		out[i] = base
		sum += base
	}
	out[n-1] += docxWidth - sum
	return out
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }

func utf8Len(s string) int { return len([]rune(s)) }

func richRuns(text, color string, sz int, bold bool) string {
	var b strings.Builder
	emit := func(s string, code, strong bool) {
		if s == "" {
			return
		}
		b.WriteString(`<w:r><w:rPr>` + bodyFonts())
		if code {
			b.WriteString(`<w:rFonts w:ascii="Consolas" w:hAnsi="Consolas"/><w:shd w:val="clear" w:fill="` + docxQuote + `"/>`)
		}
		if bold || strong {
			b.WriteString(`<w:b/>`)
		}
		b.WriteString(fmt.Sprintf(`<w:sz w:val="%d"/><w:szCs w:val="%d"/><w:color w:val="%s"/>`, sz, sz, color))
		b.WriteString(`</w:rPr><w:t xml:space="preserve">` + xmlText(s) + `</w:t></w:r>`)
	}
	rest := text
	for rest != "" {
		i, j, kind := nextRich(rest)
		if i < 0 {
			emit(rest, false, false)
			break
		}
		emit(rest[:i], false, false)
		emit(rest[i+kindLead(kind):j-kindLead(kind)], kind == "`", kind == "**")
		rest = rest[j:]
	}
	return b.String()
}

func kindLead(kind string) int {
	if kind == "**" {
		return 2
	}
	return 1
}

func nextRich(s string) (int, int, string) {
	bi := strings.Index(s, "**")
	ci := strings.Index(s, "`")
	if bi < 0 && ci < 0 {
		return -1, -1, ""
	}
	if ci >= 0 && (bi < 0 || ci < bi) {
		end := strings.Index(s[ci+1:], "`")
		if end < 0 {
			return -1, -1, ""
		}
		return ci, ci + 1 + end + 1, "`"
	}
	end := strings.Index(s[bi+2:], "**")
	if end < 0 {
		return -1, -1, ""
	}
	return bi, bi + 2 + end + 2, "**"
}

func docxContentTypes() string {
	return `<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Default Extension="png" ContentType="image/png"/><Default Extension="jpg" ContentType="image/jpeg"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/><Override PartName="/word/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.styles+xml"/><Override PartName="/word/numbering.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.numbering+xml"/><Override PartName="/word/fontTable.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.fontTable+xml"/><Override PartName="/word/header1.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.header+xml"/><Override PartName="/word/footer1.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.footer+xml"/></Types>`
}

func docxFontTable() string {
	return `<?xml version="1.0" encoding="UTF-8"?><w:fonts xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:font w:name="宋体"><w:altName w:val="SimSun"/><w:charset w:val="86"/><w:family w:val="auto"/></w:font><w:font w:name="黑体"><w:altName w:val="SimHei"/><w:charset w:val="86"/><w:family w:val="modern"/></w:font><w:font w:name="微软雅黑"><w:altName w:val="Microsoft YaHei"/><w:charset w:val="86"/><w:family w:val="swiss"/></w:font><w:font w:name="华文中宋"><w:altName w:val="STZhongsong"/><w:charset w:val="86"/><w:family w:val="roman"/></w:font><w:font w:name="Calibri"><w:family w:val="roman"/></w:font><w:font w:name="Calibri Light"><w:family w:val="swiss"/></w:font><w:font w:name="Calibri"><w:family w:val="swiss"/></w:font></w:fonts>`
}

func docxStyles() string {
	return `<?xml version="1.0" encoding="UTF-8"?><w:styles xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:docDefaults><w:rPrDefault><w:rPr>` + bodyFonts() + `<w:sz w:val="24"/><w:szCs w:val="24"/><w:color w:val="` + docxInk + `"/></w:rPr></w:rPrDefault><w:pPrDefault><w:pPr><w:spacing w:after="160" w:line="360" w:lineRule="auto"/></w:pPr></w:pPrDefault></w:docDefaults><w:style w:type="paragraph" w:default="1" w:styleId="Normal"><w:name w:val="Normal"/><w:qFormat/><w:pPr><w:ind w:firstLine="0"/><w:widowControl/></w:pPr><w:rPr>` + bodyFonts() + `</w:rPr></w:style><w:style w:type="paragraph" w:styleId="Title"><w:name w:val="Title"/><w:basedOn w:val="Normal"/><w:qFormat/><w:pPr><w:ind w:firstLine="0"/><w:spacing w:before="0" w:after="40"/></w:pPr><w:rPr>` + coverTitleFonts() + `<w:sz w:val="44"/></w:rPr></w:style><w:style w:type="paragraph" w:styleId="Heading1"><w:name w:val="heading 1"/><w:basedOn w:val="Normal"/><w:next w:val="Normal"/><w:qFormat/><w:pPr><w:keepNext/><w:ind w:firstLine="0"/><w:spacing w:before="400" w:after="160" w:line="276" w:lineRule="auto"/></w:pPr><w:rPr>` + headingFonts() + `<w:b/><w:sz w:val="32"/><w:szCs w:val="32"/><w:color w:val="` + docxNavy + `"/></w:rPr></w:style><w:style w:type="paragraph" w:styleId="Heading2"><w:name w:val="heading 2"/><w:basedOn w:val="Normal"/><w:next w:val="Normal"/><w:qFormat/><w:pPr><w:keepNext/><w:ind w:firstLine="0"/><w:spacing w:before="320" w:after="120" w:line="276" w:lineRule="auto"/><w:pBdr><w:left w:val="thick" w:sz="18" w:space="10" w:color="` + docxTeal + `"/></w:pBdr></w:pPr><w:rPr>` + headingFonts() + `<w:b/><w:sz w:val="26"/><w:szCs w:val="26"/><w:color w:val="` + docxNavy + `"/></w:rPr></w:style><w:style w:type="paragraph" w:styleId="Heading3"><w:name w:val="heading 3"/><w:basedOn w:val="Normal"/><w:next w:val="Normal"/><w:qFormat/><w:pPr><w:keepNext/><w:ind w:firstLine="0"/><w:spacing w:before="220" w:after="80"/></w:pPr><w:rPr>` + headingFonts() + `<w:b/><w:sz w:val="22"/><w:szCs w:val="22"/><w:color w:val="` + docxInk + `"/></w:rPr></w:style><w:style w:type="paragraph" w:styleId="Caption"><w:name w:val="Caption"/><w:basedOn w:val="Normal"/><w:qFormat/><w:pPr><w:ind w:firstLine="0"/><w:jc w:val="center"/><w:spacing w:before="40" w:after="200"/></w:pPr><w:rPr>` + uiFonts() + `<w:i/><w:sz w:val="18"/><w:color w:val="` + docxMuted + `"/></w:rPr></w:style><w:style w:type="paragraph" w:styleId="ListBullet"><w:name w:val="List Bullet"/><w:basedOn w:val="Normal"/><w:qFormat/><w:pPr><w:ind w:firstLine="0" w:left="420" w:hanging="210"/><w:spacing w:after="80"/></w:pPr></w:style></w:styles>`
}

func docxNumbering() string {
	return `<?xml version="1.0" encoding="UTF-8"?><w:numbering xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:abstractNum w:abstractNumId="0"><w:lvl w:ilvl="0"><w:start w:val="1"/><w:numFmt w:val="bullet"/><w:lvlText w:val="•"/><w:lvlJc w:val="left"/><w:pPr><w:ind w:left="420" w:hanging="210"/></w:pPr><w:rPr>` + rFonts() + `<w:color w:val="` + docxTeal + `"/></w:rPr></w:lvl></w:abstractNum><w:num w:numId="1"><w:abstractNumId w:val="0"/></w:num></w:numbering>`
}

func docxHeader() string {
	return `<?xml version="1.0" encoding="UTF-8"?><w:hdr xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:p><w:pPr><w:pBdr><w:bottom w:val="single" w:sz="8" w:space="8" w:color="` + docxGold + `"/></w:pBdr><w:tabs><w:tab w:val="right" w:pos="` + itoa(docxWidth) + `"/></w:tabs><w:spacing w:after="80"/></w:pPr><w:r><w:rPr>` + headingFonts() + `<w:sz w:val="18"/><w:color w:val="` + docxNavy + `"/></w:rPr><w:t>DeepSentry 安全巡检报告</w:t></w:r><w:r><w:tab/></w:r><w:r><w:rPr>` + uiFonts() + `<w:sz w:val="16"/><w:color w:val="` + docxMuted + `"/></w:rPr><w:t>内部资料</w:t></w:r></w:p></w:hdr>`
}

func docxFooter() string {
	return `<?xml version="1.0" encoding="UTF-8"?><w:ftr xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:p><w:pPr><w:pBdr><w:top w:val="single" w:sz="6" w:space="8" w:color="` + docxLine + `"/></w:pBdr><w:tabs><w:tab w:val="right" w:pos="` + itoa(docxWidth) + `"/></w:tabs><w:spacing w:before="60"/></w:pPr><w:r><w:rPr>` + uiFonts() + `<w:sz w:val="16"/><w:color w:val="` + docxMuted + `"/></w:rPr><w:t>由 DeepSentry 自动生成，仅供授权人员阅览</w:t></w:r><w:r><w:tab/></w:r><w:r><w:rPr>` + uiFonts() + `<w:sz w:val="16"/><w:color w:val="` + docxMuted + `"/></w:rPr><w:t>第 </w:t></w:r><w:r><w:fldChar w:fldCharType="begin"/></w:r><w:r><w:instrText xml:space="preserve"> PAGE </w:instrText></w:r><w:r><w:fldChar w:fldCharType="end"/></w:r><w:r><w:rPr>` + uiFonts() + `<w:sz w:val="16"/><w:color w:val="` + docxMuted + `"/></w:rPr><w:t> 页</w:t></w:r></w:p></w:ftr>`
}
