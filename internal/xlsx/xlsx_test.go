package xlsx

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"io"
	"strings"
	"testing"
)

// 生成的工作簿必须是可解析的 zip：六个部件齐全、XML 合法，中文/转义/数字单元格都在。
func TestWriteWorkbook(t *testing.T) {
	rows := [][]Cell{
		{Text("账单编号"), Text("金额")},
		{Text("INV-1 & <测试>"), Number(12.3)},
		{Text(""), Number(0)},
	}
	b, err := Write(rows)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatalf("zip: %v", err)
	}
	files := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %s: %v", f.Name, err)
		}
		data, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatalf("read %s: %v", f.Name, err)
		}
		files[f.Name] = string(data)
	}
	for _, name := range []string{"[Content_Types].xml", "_rels/.rels", "xl/workbook.xml", "xl/_rels/workbook.xml.rels", "xl/styles.xml", "xl/worksheets/sheet1.xml"} {
		part, ok := files[name]
		if !ok || part == "" {
			t.Fatalf("missing part %s", name)
		}
		dec := xml.NewDecoder(strings.NewReader(part))
		for {
			if _, err := dec.Token(); err == io.EOF {
				break
			} else if err != nil {
				t.Fatalf("part %s is not valid xml: %v", name, err)
			}
		}
	}
	sheet := files["xl/worksheets/sheet1.xml"]
	if !strings.Contains(sheet, "INV-1 &amp; &lt;测试&gt;") {
		t.Fatalf("sheet does not contain escaped text: %s", sheet)
	}
	if !strings.Contains(sheet, `<c r="B2" s="2"><v>12.30</v></c>`) {
		t.Fatalf("number cell missing: %s", sheet)
	}
	if !strings.Contains(sheet, `<c r="A1" s="1"`) {
		t.Fatalf("header should use the bold style: %s", sheet)
	}
}

// 列名进位：A..Z, AA..AZ, BA, ZZ, AAA。
func TestColName(t *testing.T) {
	cases := map[int]string{0: "A", 25: "Z", 26: "AA", 27: "AB", 51: "AZ", 52: "BA", 701: "ZZ", 702: "AAA"}
	for i, want := range cases {
		if got := colName(i); got != want {
			t.Fatalf("colName(%d) = %s, want %s", i, got, want)
		}
	}
}

// XML 非法控制字符被丢弃，引号/尖括号被转义。
func TestEscapeXML(t *testing.T) {
	if got := escapeXML("a\"b'c<d>&\x01e"); got != "a&quot;b&apos;c&lt;d&gt;&amp;e" {
		t.Fatalf("escapeXML = %s", got)
	}
}
