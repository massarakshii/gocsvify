package xlsx

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveSheetPath(t *testing.T) {
	t.Parallel()

	tests := []struct{ target, want string }{
		{"worksheets/sheet1.xml", "xl/worksheets/sheet1.xml"},
		{"/xl/worksheets/sheet1.xml", "xl/worksheets/sheet1.xml"},
		{"./worksheets/sheet1.xml", "xl/worksheets/sheet1.xml"},
		{"../xl/worksheets/sheet1.xml", "xl/worksheets/sheet1.xml"},
		{"worksheets/../worksheets/sheet1.xml", "xl/worksheets/sheet1.xml"},
		{"/custom/data.xml", "custom/data.xml"},
		{"sheets/report.xml", "xl/sheets/report.xml"},
	}
	for _, tt := range tests {
		if got := resolveSheetPath(tt.target); got != tt.want {
			t.Errorf("resolveSheetPath(%q) = %q, want %q", tt.target, got, tt.want)
		}
	}
}

// Target в rels бывает абсолютным, с нестандартным именем файла или каталогом.
func TestConvertSheetTargetForms(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		target string
		file   string
	}{
		{"absolute path", "/xl/worksheets/sheet1.xml", "xl/worksheets/sheet1.xml"},
		{"dot prefix", "./worksheets/sheet1.xml", "xl/worksheets/sheet1.xml"},
		{"parent reference", "../xl/worksheets/sheet1.xml", "xl/worksheets/sheet1.xml"},
		{"custom file name", "worksheets/data.xml", "xl/worksheets/data.xml"},
		{"custom directory", "/xl/sheets/report.xml", "xl/sheets/report.xml"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rels := xmlDecl + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
				`<Relationship Id="rId1" Type="` + relNS + `/worksheet" Target="` + tt.target + `"/></Relationships>`
			path := writeXlsx(t,
				zipEntry{"xl/workbook.xml", workbookXMLSheets(``, `<sheet name="S" sheetId="1" r:id="rId1"/>`)},
				zipEntry{"xl/_rels/workbook.xml.rels", rels},
				zipEntry{tt.file, sheetXML("A1", rowXML(`<c r="A1"><v>42</v></c>`))},
			)
			outDir, err := runConvert(t, path, ";")
			if err != nil {
				t.Fatalf("Convert failed: %v", err)
			}
			if got := readFile(t, filepath.Join(outDir, "0.csv")); got != "42\n" {
				t.Errorf("0.csv = %q, want %q", got, "42\n")
			}
		})
	}
}

// Связи не-листов (стили, sharedStrings, темы) не принимаются за листы.
func TestConvertIgnoresNonWorksheetRelationships(t *testing.T) {
	t.Parallel()

	rels := xmlDecl + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
		`<Relationship Id="rId1" Type="` + relNS + `/worksheet" Target="/xl/worksheets/sheet1.xml"/>` +
		`<Relationship Id="rId2" Type="` + relNS + `/styles" Target="styles.xml"/>` +
		`<Relationship Id="rId3" Type="` + relNS + `/theme" Target="theme/theme1.xml"/></Relationships>`
	path := writeXlsx(t,
		zipEntry{"xl/workbook.xml", workbookXMLSheets(``,
			`<sheet name="S" sheetId="1" r:id="rId1"/>`,
			`<sheet name="X" sheetId="2" r:id="rId2"/>`)},
		zipEntry{"xl/_rels/workbook.xml.rels", rels},
		zipEntry{"xl/styles.xml", convertStyles},
		zipEntry{"xl/theme/theme1.xml", `<a:theme xmlns:a="x"/>`},
		zipEntry{"xl/worksheets/sheet1.xml", sheetXML("A1", rowXML(`<c r="A1"><v>1</v></c>`))},
	)
	outDir, err := runConvert(t, path, ";")
	if err != nil {
		t.Fatalf("Convert failed: %v", err)
	}
	if got, want := listDir(t, outDir), []string{"0.csv"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("output files = %v, want %v", got, want)
	}
}

// Без workbook.xml листами считаются только xl/worksheets/sheet*.xml, а не любые xml-файлы архива.
func TestConvertWithoutWorkbookIgnoresOtherXmlParts(t *testing.T) {
	t.Parallel()

	path := writeXlsx(t,
		zipEntry{"[Content_Types].xml", xmlDecl + `<Types/>`},
		zipEntry{"docProps/app.xml", `<Properties/>`},
		zipEntry{"xl/theme/theme1.xml", `<theme/>`},
		zipEntry{"xl/worksheets/sheet1.xml", sheetXML("A1", rowXML(`<c r="A1"><v>1</v></c>`))},
	)
	outDir, err := runConvert(t, path, ";")
	if err != nil {
		t.Fatalf("Convert failed: %v", err)
	}
	if got, want := listDir(t, outDir), []string{"0.csv"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("output files = %v, want %v", got, want)
	}
}
