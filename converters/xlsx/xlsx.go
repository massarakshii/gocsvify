package xlsx

import (
	"archive/zip"
	"context"
	"encoding/csv"
	"encoding/xml"
	"fmt"
	"gocsvify/settings"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

type Converter struct {
	Ctx       context.Context
	FilePath  string
	DigitKeys map[string]int
	StartDate string
	Settings  *settings.Settings
}

type Cell struct {
	Type        string        `xml:"t,attr"` // Атрибут t (тип данных, например "s" для строк)
	Style       string        `xml:"s,attr"` // Атрибут s (стиль данных, например число как дата)
	Column      string        `xml:"r,attr"`
	Value       string        `xml:"v"` // Вложенный тег <v> со значением или индексом
	InlineValue *InlineString `xml:"is"`
}

type InlineString struct {
	Value     string     `xml:"t"`
	RichValue []RichText `xml:"r"`
}
type RichText struct {
	Value string `xml:"t"` // Текст внутри кусочка форматирования
}

type Dimension struct {
	Ref string `xml:"ref,attr"`
}
type WorkBookPr struct {
	Date1904 string `xml:"date1904,attr"`
}

const defaultDelimiter = ";"

func NewConverter(ctx context.Context, filePath string, settings *settings.Settings) *Converter {
	return &Converter{
		Ctx:       ctx,
		FilePath:  filePath,
		DigitKeys: make(map[string]int),
		StartDate: "1899-12-30",
		Settings:  settings,
	}
}

func (u *Converter) Convert(csvPath string) error {
	reader, err := zip.OpenReader(u.FilePath)
	if err != nil {
		return err
	}
	defer reader.Close()

	var sharedStrings []string
	var xlsxStyler *XlsxStyler
	sheetFiles := make(map[string]*zip.File)

	var sheetsRelation map[string]string
	var sheets []string
	hasWorkbook := false
	for _, f := range reader.File {
		if f.Name == "xl/sharedStrings.xml" {
			sharedStrings, err = u.parseSharedStrings(f)
			if err != nil {
				return fmt.Errorf("ошибка парсинга словаря строк: %w", err)
			}
		}
		if f.Name == "xl/workbook.xml" {
			u.StartDate, err = u.getStartDate(f)
			if err != nil {
				return fmt.Errorf("ошибка получения стартовой даты для форматирования: %w", err)
			}
		}
		if f.Name == "xl/styles.xml" {
			xlsxStyler, err = NewXlsxStyler(f)
			if err != nil {
				return fmt.Errorf("ошибка создания стилей: %w", err)
			}
		}
		if f.Name == "xl/_rels/workbook.xml.rels" {
			sheetsRelation, err = u.getSheetsRelation(f)
			if err != nil {
				return fmt.Errorf("ошибка получения списка листов: %w", err)
			}
		}
		if f.Name == "xl/workbook.xml" {
			hasWorkbook = true
			sheets, err = u.getSheets(f, u.Settings.WithHidden)
			if err != nil {
				return fmt.Errorf("ошибка получения списка листов: %w", err)
			}
		}

		if strings.HasPrefix(f.Name, "xl/worksheets/sheet") && strings.HasSuffix(f.Name, ".xml") {
			sheetFiles[f.Name] = f
		}
	}

	i := 0
	if hasWorkbook {
		for _, rId := range sheets {
			sheetName := sheetsRelation[rId]
			sheetFullName := filepath.Join("xl", sheetName)
			if _, ok := sheetFiles[sheetFullName]; !ok {
				continue
			}
			err = u.saveSheet(sheetFiles[sheetFullName], i, sharedStrings, xlsxStyler, csvPath)
			if err != nil {
				return fmt.Errorf("ошибка создания CSV: %w", err)
			}
			delete(sheetFiles, sheetFullName)
			i++
		}
	} else {
		names := make([]string, 0, len(sheetFiles))
		for name := range sheetFiles {
			names = append(names, name)
		}
		sheetNum := func(name string) int {
			s := strings.TrimSuffix(strings.TrimPrefix(name, "xl/worksheets/sheet"), ".xml")
			n, err := strconv.Atoi(s)
			if err != nil {
				return math.MaxInt
			}
			return n
		}
		sort.Slice(names, func(i, j int) bool {
			ni, nj := sheetNum(names[i]), sheetNum(names[j])
			if ni != nj {
				return ni < nj
			}
			return names[i] < names[j]
		})

		for _, sheetName := range names {
			err = u.saveSheet(sheetFiles[sheetName], i, sharedStrings, xlsxStyler, csvPath)
			if err != nil {
				return fmt.Errorf("ошибка создания CSV: %w", err)
			}
			i++
		}
	}

	return nil
}

func (u *Converter) saveSheet(sheetFile *zip.File, sheetNum int, sharedStrings []string, xlsxStyler *XlsxStyler, csvPath string) (err error) {
	outFile, err := os.Create(filepath.Join(csvPath, strconv.Itoa(sheetNum)+".csv"))
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := outFile.Close(); err == nil {
			err = closeErr
		}
	}()

	delimiter := u.Settings.Delimiter
	if delimiter == "" {
		delimiter = defaultDelimiter
	}
	writer := csv.NewWriter(outFile)
	writer.Comma = []rune(delimiter)[0]

	err = u.sheetToCsv(sheetFile, sharedStrings, writer, xlsxStyler)
	if err != nil {
		errRemove := os.Remove(outFile.Name())
		if errRemove != nil {
			slog.Error("ошибка удаления файла", "err", errRemove)
		}
		return err
	}

	writer.Flush()

	return writer.Error()
}

func (u *Converter) getStartDate(f *zip.File) (string, error) {
	startDate := "1899-12-30"
	rc, err := f.Open()
	if err != nil {
		return startDate, err
	}
	defer rc.Close()

	decoder := xml.NewDecoder(rc)

	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return startDate, err
		}

		node, ok := token.(xml.StartElement)
		if ok && node.Name.Local == "workbookPr" {
			var val WorkBookPr
			if err := decoder.DecodeElement(&val, &node); err != nil {
				return startDate, err
			}

			if val.Date1904 == "1" || strings.EqualFold(val.Date1904, "true") {
				startDate = "1904-01-01"
			}
		}
	}
	return startDate, nil
}

func (u *Converter) parseSharedStrings(f *zip.File) ([]string, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	var records []string
	decoder := xml.NewDecoder(rc)

	var str strings.Builder
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}

		switch node := token.(type) {
		case xml.StartElement:
			switch node.Name.Local {
			case "si":
				str = strings.Builder{}
			case "rPh":
				if err := decoder.Skip(); err != nil {
					return nil, err
				}
			case "t":
				var val string
				if err := decoder.DecodeElement(&val, &node); err != nil {
					return nil, err
				}
				str.WriteString(val)
			}
		case xml.EndElement:
			if node.Name.Local == "si" {
				records = append(records, str.String())
			}
		}
	}
	return records, nil
}

func (u *Converter) formatDate(date float64) string {
	is1900 := u.StartDate == "1899-12-30"
	baseDate := u.StartDate
	daysSinceBaseDate := int(date)
	timeRemainder := math.Mod(date, 1)
	secondsRemainder := int(math.Round(timeRemainder * 86400))

	if is1900 {
		// баг 29.02.1900
		baseDate = "1899-12-31"
		if daysSinceBaseDate >= 60 {
			daysSinceBaseDate -= 1
		}
	}

	newDate, _ := time.Parse("2006-01-02", baseDate)
	newDate = newDate.AddDate(0, 0, daysSinceBaseDate)
	newDate = newDate.Add(time.Duration(secondsRemainder) * time.Second)

	format := "2006-01-02 15:04:05"
	if daysSinceBaseDate == 0 {
		format = "15:04:05"
	} else if secondsRemainder == 0 {
		format = "2006-01-02"
	}
	return newDate.Format(format)
}

func (u *Converter) sheetToCsv(file *zip.File, sharedStrings []string, writer *csv.Writer, styler *XlsxStyler) error {
	rc, err := file.Open()
	if err != nil {
		return err
	}
	defer rc.Close()

	decoder := xml.NewDecoder(rc)
	var row []string
	startColumnIndex := u.getDigitKey("A")
	endColumnIndex := u.getDigitKey("Z")
	var currentColumnIndex int

	for {
		if u.Ctx.Err() != nil {
			return u.Ctx.Err()
		}

		token, err := decoder.Token()
		if err == io.EOF {
			break
		}

		if err != nil {
			return err
		}

		switch se := token.(type) {
		case xml.StartElement:
			switch se.Name.Local {
			case "dimension":
				var dimension Dimension
				if err := decoder.DecodeElement(&dimension, &se); err != nil {
					return err
				}
				start, end, ok := strings.Cut(dimension.Ref, ":")
				if ok {
					startColumnIndex = u.getDigitKey(start)
					endColumnIndex = u.getDigitKey(end)
				} else {
					startColumnIndex = u.getDigitKey(dimension.Ref)
					endColumnIndex = u.getDigitKey(dimension.Ref)
				}
			case "row":
				row = []string{}
				currentColumnIndex = startColumnIndex
				break
			case "c":
				var c Cell
				if err := decoder.DecodeElement(&c, &se); err != nil {
					return err
				}

				colIndex := currentColumnIndex
				if c.Column != "" {
					colIndex = u.getDigitKey(c.Column)
					for colIndex > currentColumnIndex {
						row = append(row, "")
						currentColumnIndex++
					}
				}
				switch c.Type {
				case "s":
					val := ""
					idx, err := strconv.Atoi(c.Value)
					if err == nil && idx < len(sharedStrings) {
						val = sharedStrings[idx]
					}
					row = append(row, val)
				case "inlineStr":
					var sb strings.Builder
					if c.InlineValue != nil && c.InlineValue.Value != "" {
						sb.WriteString(c.InlineValue.Value)
					}

					if c.InlineValue != nil && c.InlineValue.RichValue != nil {
						for _, r := range c.InlineValue.RichValue {
							sb.WriteString(r.Value)
						}
					}
					row = append(row, sb.String())
				default:
					styleId, err := strconv.Atoi(c.Style)
					if err == nil && c.Value != "" && styler != nil && styler.CheckStyleDate(styleId) {
						floatVal, err := strconv.ParseFloat(c.Value, 64)
						if err != nil {
							row = append(row, c.Value)
						} else {
							row = append(row, u.formatDate(floatVal))
						}
					} else {
						row = append(row, c.Value)
					}
				}
				currentColumnIndex++
				break
			}
			break
		case xml.EndElement:
			if se.Name.Local == "row" {
				if u.Settings.PadRows {
					for currentColumnIndex <= endColumnIndex {
						row = append(row, "")
						currentColumnIndex++
					}
				}
				// Строка закрылась </row> — сбрасываем её в CSV поток
				if err := writer.Write(row); err != nil {
					return err
				}
			}
		}
	}

	return nil
}

type Relationship struct {
	Target string `xml:"Target,attr"`
	Id     string `xml:"Id,attr"`
}

func (u *Converter) getSheetsRelation(file *zip.File) (map[string]string, error) {
	rc, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	decoder := xml.NewDecoder(rc)

	data := make(map[string]string)
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}

		node, ok := token.(xml.StartElement)
		if !ok {
			continue
		}

		switch node.Name.Local {
		case "Relationship":
			var rs Relationship
			if err := decoder.DecodeElement(&rs, &node); err != nil {
				return nil, err
			}
			if strings.HasPrefix(rs.Target, "worksheets/sheet") && strings.HasSuffix(rs.Target, ".xml") {
				data[rs.Id] = rs.Target
			}
		}
	}

	return data, nil
}

func (u *Converter) getSheets(file *zip.File, withHidden bool) ([]string, error) {
	rc, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	decoder := xml.NewDecoder(rc)

	var rNamespace string
	var data []string
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}

		node, ok := token.(xml.StartElement)
		if !ok {
			continue
		}

		switch node.Name.Local {
		case "workbook":
			for _, attr := range node.Attr {
				if attr.Name.Space == "xmlns" && attr.Name.Local == "r" {
					rNamespace = attr.Value
				}
			}
		case "sheet":
			var rId string
			var state string
			for _, attr := range node.Attr {
				if attr.Name.Local == "id" && attr.Name.Space == rNamespace {
					rId = attr.Value
				}
				if attr.Name.Local == "state" {
					state = attr.Value
				}
			}
			if rId == "" || (state == "hidden" && !withHidden) {
				continue
			}
			data = append(data, rId)
		}
	}

	return data, nil
}

func (u *Converter) getDigitKey(key string) int {
	key = prepareColumnKey(key)

	if digitKey, ok := u.DigitKeys[key]; ok {
		return digitKey
	}
	startIndex := int(rune('A'))
	length := float64(len(key) - 1)
	digitKey := 0
	for _, ch := range key {
		digitKey += ((int(rune(ch)) - startIndex) + 1) * int(math.Pow(26.0, length))
		length--
	}
	u.DigitKeys[key] = digitKey

	return digitKey
}

func prepareColumnKey(key string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsDigit(r) {
			return -1
		}
		return r
	}, key)
}
