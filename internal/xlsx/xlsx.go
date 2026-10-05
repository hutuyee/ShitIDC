// Package xlsx writes a minimal Excel (.xlsx) workbook with a single sheet.
//
// 魔方 export_excel 插件用 PhpSpreadsheet 生成 xlsx；ShitIDC 保持纯标准库，
// 直接按 OOXML 最小结构拼 zip：内联字符串 + 数字单元格 + 加粗表头。
package xlsx

import (
	"archive/zip"
	"bytes"
	"strconv"
	"strings"
)

// Cell is one spreadsheet cell. Number wins when set, so money cells become
// real numbers instead of text.
type Cell struct {
	Text   string
	Number *float64
}

// Text builds a text cell.
func Text(s string) Cell { return Cell{Text: s} }

// Number builds a numeric cell (written with a 0.00 format).
func Number(f float64) Cell { return Cell{Number: &f} }

// Write renders rows into one sheet; the first row is the bold header.
func Write(rows [][]Cell) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	files := []struct {
		name string
		body string
	}{
		{"[Content_Types].xml", contentTypes},
		{"_rels/.rels", rootRels},
		{"xl/workbook.xml", workbook},
		{"xl/_rels/workbook.xml.rels", workbookRels},
		{"xl/styles.xml", styles},
		{"xl/worksheets/sheet1.xml", sheetXML(rows)},
	}
	for _, f := range files {
		w, err := zw.Create(f.name)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write([]byte(f.body)); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func sheetXML(rows [][]Cell) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
	b.WriteString(`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>`)
	for ri, row := range rows {
		rowNum := ri + 1
		b.WriteString(`<row r="` + strconv.Itoa(rowNum) + `">`)
		for ci, cell := range row {
			ref := colName(ci) + strconv.Itoa(rowNum)
			if cell.Number != nil {
				b.WriteString(`<c r="` + ref + `" s="2"><v>` + strconv.FormatFloat(*cell.Number, 'f', 2, 64) + `</v></c>`)
				continue
			}
			text := escapeXML(cell.Text)
			if text == "" {
				continue
			}
			style := "0"
			if ri == 0 {
				style = "1"
			}
			b.WriteString(`<c r="` + ref + `" s="` + style + `" t="inlineStr"><is><t xml:space="preserve">` + text + `</t></is></c>`)
		}
		b.WriteString(`</row>`)
	}
	b.WriteString(`</sheetData></worksheet>`)
	return b.String()
}

// colName maps a zero-based column index to A, B, ... Z, AA, ... .
func colName(i int) string {
	var b []byte
	for i >= 0 {
		b = append([]byte{byte('A' + i%26)}, b...)
		i = i/26 - 1
	}
	return string(b)
}

// escapeXML escapes text and drops control characters that XML 1.0 rejects.
func escapeXML(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		case '\'':
			b.WriteString("&apos;")
		default:
			if r < 0x20 && r != '\t' && r != '\n' && r != '\r' {
				continue
			}
			b.WriteRune(r)
		}
	}
	return b.String()
}

const contentTypes = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
	`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
	`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
	`<Default Extension="xml" ContentType="application/xml"/>` +
	`<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>` +
	`<Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>` +
	`<Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>` +
	`</Types>`

const rootRels = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
	`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
	`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>` +
	`</Relationships>`

const workbook = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
	`<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">` +
	`<sheets><sheet name="Sheet1" sheetId="1" r:id="rId1"/></sheets></workbook>`

const workbookRels = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
	`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
	`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>` +
	`<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>` +
	`</Relationships>`

const styles = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
	`<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">` +
	`<fonts count="2">` +
	`<font><sz val="11"/><name val="Calibri"/></font>` +
	`<font><b/><sz val="11"/><name val="Calibri"/></font>` +
	`</fonts>` +
	`<fills count="2"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill></fills>` +
	`<borders count="1"><border><left/><right/><top/><bottom/><diagonal/></border></borders>` +
	`<cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs>` +
	`<cellXfs count="3">` +
	`<xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/>` +
	`<xf numFmtId="0" fontId="1" fillId="0" borderId="0" xfId="0" applyFont="1"/>` +
	`<xf numFmtId="4" fontId="0" fillId="0" borderId="0" xfId="0" applyNumberFormat="1"/>` +
	`</cellXfs>` +
	`<cellStyles count="1"><cellStyle name="Normal" xfId="0" builtinId="0"/></cellStyles>` +
	`</styleSheet>`
