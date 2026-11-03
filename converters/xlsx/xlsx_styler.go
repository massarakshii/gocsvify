package xlsx

import (
	"archive/zip"
	"encoding/xml"
	"io"
	"regexp"
	"strings"
)

type XlsxStyler struct {
	file       *zip.File
	DateStyles map[int]string
	Styles     CellXfs
	Nfts       map[int]string
}

type NumFmts struct {
	Nf []NumFmt `xml:"numFmt"`
}

type NumFmt struct {
	NumFmtId   int    `xml:"numFmtId,attr"`
	FormatCode string `xml:"formatCode,attr"`
}

type CellXfs struct {
	Xf []CellXf `xml:"xf"`
}

type CellXf struct {
	NumFmtId int `xml:"numFmtId,attr"`
}

func NewXlsxStyler(file *zip.File) (*XlsxStyler, error) {
	styler := XlsxStyler{
		file: file,
		DateStyles: map[int]string{
			14: "m/d/yyyy", // @NOTE: ECMA spec is 'mm-dd-yy'
			15: "d-mmm-yy",
			16: "d-mmm",
			17: "mmm-yy",
			18: "h:mm AM/PM",
			19: "h:mm:ss AM/PM",
			20: "h:mm",
			21: "h:mm:ss",
			22: "m/d/yyyy h:mm", // @NOTE: ECMA spec is 'm/d/yy h:mm',
			45: "mm:ss",
			46: "[h]:mm:ss",
			47: "mm:ss.0",
		},
	}
	err := styler.SetStyles()
	if err != nil {
		return nil, err
	}

	return &styler, nil
}

func (s *XlsxStyler) SetStyles() error {
	rc, err := s.file.Open()
	if err != nil {
		return err
	}
	defer rc.Close()

	s.Nfts = make(map[int]string)

	var styles CellXfs
	decoder := xml.NewDecoder(rc)

	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		node, ok := token.(xml.StartElement)
		if !ok {
			continue
		}

		switch node.Name.Local {
		case "numFmts":
			var nfts NumFmts
			if err := decoder.DecodeElement(&nfts, &node); err != nil {
				return err
			}

			for _, nf := range nfts.Nf {
				s.Nfts[nf.NumFmtId] = nf.FormatCode
			}
		case "cellXfs":
			if err := decoder.DecodeElement(&styles, &node); err != nil {
				return err
			}
		}
	}

	s.Styles = styles

	return nil
}

func (s *XlsxStyler) CheckDateFormatCode(formatCode *string) bool {
	if formatCode == nil || strings.EqualFold(*formatCode, "General") {
		return false
	}

	lowerCode := strings.ToLower(*formatCode)

	re := regexp.MustCompile(`(^|[^\\])\[(.+?)\]`)
	lowerCode = re.ReplaceAllString(lowerCode, `${1}`)

	dateFormatCharacters := []string{"yy", "m", "d", "h", "s"}
	for _, target := range dateFormatCharacters {
		pos := 0
		for {
			// Ищем очередное вхождение подстроки
			idx := strings.Index(lowerCode[pos:], target)
			if idx == -1 {
				break // Больше таких символов нет, переходим к следующему target
			}

			// Абсолютный индекс в оригинальной строке
			actualIdx := pos + idx

			// Проверяем, экранирован ли символ:
			// Если перед ним стоит '\' и этот '\' сам не экранирован
			if actualIdx > 0 && lowerCode[actualIdx-1] == '\\' {
				// Считаем бэкслеши перед символом, чтобы учесть случаи вроде \\m (экранированный бэкслеш + валидный символ)
				backslashCount := 0
				for i := actualIdx - 1; i >= 0 && lowerCode[i] == '\\'; i-- {
					backslashCount++
				}
				// Если количество бэкслешей нечетное, значит символ заэкранирован
				if backslashCount%2 != 0 {
					pos = actualIdx + len(target)
					continue
				}
			}

			// Если символ не заэкранирован — мы нашли то, что искали
			return true
		}
	}
	return false
}

func (s *XlsxStyler) CheckStyleDate(styleId int) bool {
	if styleId < 0 || styleId >= len(s.Styles.Xf) {
		return false
	}

	id := s.Styles.Xf[styleId].NumFmtId
	// пока что не нужно приводить к формату из стилей, по этому не получаем формат
	if _, ok := s.DateStyles[id]; ok {
		return true
	}

	if code, ok := s.Nfts[id]; ok {
		return s.CheckDateFormatCode(&code)
	}

	return false
}
