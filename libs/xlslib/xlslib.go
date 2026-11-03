package xlslib

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

// Sheet — один лист книги.
type Sheet struct {
	Name string
	Rows [][]string
}

// ReadSheets разбирает xls и возвращает все листы с данными в виде строк.
func ReadSheets(r io.Reader) ([]Sheet, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	book, err := readWorkbookStream(data)
	if err != nil {
		return nil, err
	}
	return parseWorkbook(book)
}

// ---------------------------------------------------------------------------
// OLE2 / Compound File Binary
// ---------------------------------------------------------------------------

var oleSignature = []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}

const (
	sectEnd  = 0xFFFFFFFE
	sectFree = 0xFFFFFFFF
)

type dirEntry struct {
	name  string
	typ   byte
	start uint32
	size  uint32
}

func readWorkbookStream(data []byte) ([]byte, error) {
	if len(data) < 512 || !bytes.Equal(data[:8], oleSignature) {
		return nil, errors.New("not an OLE2 (xls) file")
	}
	le := binary.LittleEndian
	shift := uint(le.Uint16(data[30:]))
	miniShift := uint(le.Uint16(data[32:]))
	if shift < 7 || shift > 16 || miniShift > shift {
		return nil, errors.New("corrupt OLE2 header")
	}
	secSize := 1 << shift
	miniSize := 1 << miniShift
	dirStart := le.Uint32(data[48:])
	miniCutoff := le.Uint32(data[56:])
	miniFatStart := le.Uint32(data[60:])
	difatStart := le.Uint32(data[68:])
	difatCount := le.Uint32(data[72:])

	sector := func(id uint32) ([]byte, error) {
		off := (int64(id) + 1) << shift
		if off+int64(secSize) > int64(len(data)) {
			// допускаем усечённый последний сектор
			if off >= int64(len(data)) {
				return nil, errors.New("sector out of range")
			}
			return data[off:], nil
		}
		return data[off : off+int64(secSize)], nil
	}

	// DIFAT -> список секторов FAT
	var fatSectors []uint32
	for i := 0; i < 109; i++ {
		v := le.Uint32(data[76+4*i:])
		if v != sectFree {
			fatSectors = append(fatSectors, v)
		}
	}
	next := difatStart
	for i := uint32(0); i < difatCount && next != sectEnd && next != sectFree; i++ {
		s, err := sector(next)
		if err != nil {
			return nil, err
		}
		n := len(s)/4 - 1
		for j := 0; j < n; j++ {
			v := le.Uint32(s[4*j:])
			if v != sectFree {
				fatSectors = append(fatSectors, v)
			}
		}
		next = le.Uint32(s[4*n:])
	}

	var fat []uint32
	for _, id := range fatSectors {
		s, err := sector(id)
		if err != nil {
			return nil, err
		}
		for j := 0; j+4 <= len(s); j += 4 {
			fat = append(fat, le.Uint32(s[j:]))
		}
	}

	readChain := func(table []uint32, start uint32, get func(uint32) ([]byte, error)) ([]byte, error) {
		var buf bytes.Buffer
		seen := 0
		for id := start; id != sectEnd && id != sectFree; {
			if int(id) >= len(table) {
				return nil, errors.New("bad sector chain")
			}
			s, err := get(id)
			if err != nil {
				return nil, err
			}
			buf.Write(s)
			id = table[id]
			if seen++; seen > len(table)+1 {
				return nil, errors.New("cyclic sector chain")
			}
		}
		return buf.Bytes(), nil
	}

	dirData, err := readChain(fat, dirStart, sector)
	if err != nil {
		return nil, err
	}

	var entries []dirEntry
	for i := 0; i+128 <= len(dirData); i += 128 {
		e := dirData[i : i+128]
		nl := int(le.Uint16(e[64:]))
		if nl < 2 || nl > 64 {
			entries = append(entries, dirEntry{})
			continue
		}
		u := make([]uint16, nl/2-1)
		for k := range u {
			u[k] = le.Uint16(e[2*k:])
		}
		entries = append(entries, dirEntry{
			name:  string(utf16.Decode(u)),
			typ:   e[66],
			start: le.Uint32(e[116:]),
			size:  le.Uint32(e[120:]),
		})
	}
	if len(entries) == 0 {
		return nil, errors.New("empty OLE2 directory")
	}

	var target *dirEntry
	for i := range entries {
		if entries[i].typ == 2 && (entries[i].name == "Workbook" || entries[i].name == "Book") {
			target = &entries[i]
			break
		}
	}
	if target == nil {
		return nil, errors.New("Workbook stream not found")
	}

	if target.size >= miniCutoff {
		b, err := readChain(fat, target.start, sector)
		if err != nil {
			return nil, err
		}
		if int(target.size) < len(b) {
			b = b[:target.size]
		}
		return b, nil
	}

	// мини-поток
	root := entries[0]
	miniStream, err := readChain(fat, root.start, sector)
	if err != nil {
		return nil, err
	}
	miniFatData, err := readChain(fat, miniFatStart, sector)
	if err != nil {
		return nil, err
	}
	miniFat := make([]uint32, len(miniFatData)/4)
	for i := range miniFat {
		miniFat[i] = le.Uint32(miniFatData[4*i:])
	}
	b, err := readChain(miniFat, target.start, func(id uint32) ([]byte, error) {
		off := int(id) * miniSize
		if off+miniSize > len(miniStream) {
			return nil, errors.New("mini sector out of range")
		}
		return miniStream[off : off+miniSize], nil
	})
	if err != nil {
		return nil, err
	}
	if int(target.size) < len(b) {
		b = b[:target.size]
	}
	return b, nil
}

// ---------------------------------------------------------------------------
// BIFF8
// ---------------------------------------------------------------------------

const (
	recEOF        = 0x000A
	recFormula    = 0x0006
	recContinue   = 0x003C
	recDateMode   = 0x0022
	recFormat     = 0x041E
	recBoundSheet = 0x0085
	recMulRK      = 0x00BD
	recNumber     = 0x0203
	recLabel      = 0x0204
	recBoolErr    = 0x0205
	recString     = 0x0207
	recRK         = 0x027E
	recSST        = 0x00FC
	recLabelSST   = 0x00FD
	recXF         = 0x00E0
	recBOF        = 0x0809
)

type record struct {
	id   uint16
	data []byte
	// chunks — данные записи вместе со следующими CONTINUE (нужно для SST)
	chunks [][]byte
}

// nextRecord читает запись по смещению pos. Следующие CONTINUE присоединяются
// в chunks.
func nextRecord(b []byte, pos int) (rec record, next int, ok bool) {
	le := binary.LittleEndian
	if pos+4 > len(b) {
		return rec, pos, false
	}
	id := le.Uint16(b[pos:])
	n := int(le.Uint16(b[pos+2:]))
	if pos+4+n > len(b) {
		return rec, pos, false
	}
	rec.id = id
	rec.data = b[pos+4 : pos+4+n]
	rec.chunks = [][]byte{rec.data}
	next = pos + 4 + n
	for next+4 <= len(b) && le.Uint16(b[next:]) == recContinue {
		cn := int(le.Uint16(b[next+2:]))
		if next+4+cn > len(b) {
			break
		}
		rec.chunks = append(rec.chunks, b[next+4:next+4+cn])
		next += 4 + cn
	}
	return rec, next, true
}

type sheetInfo struct {
	name   string
	offset int
	typ    byte
}

type workbook struct {
	sst      []string
	sheets   []sheetInfo
	dateMode int
	formats  map[uint16]string
	xfFormat []uint16
}

func parseWorkbook(b []byte) ([]Sheet, error) {
	wb := &workbook{formats: map[uint16]string{}}
	le := binary.LittleEndian

	// глобальная часть: от первого BOF до EOF
	pos := 0
	depth := 0
	for {
		rec, next, ok := nextRecord(b, pos)
		if !ok {
			break
		}
		pos = next
		switch rec.id {
		case recBOF:
			depth++
		case recEOF:
			depth--
		case recDateMode:
			if len(rec.data) >= 2 {
				wb.dateMode = int(le.Uint16(rec.data))
			}
		case recFormat:
			if len(rec.data) >= 2 {
				r := &chunkReader{chunks: [][]byte{rec.data[2:]}}
				if s, err := r.readString(2); err == nil {
					wb.formats[le.Uint16(rec.data)] = s
				}
			}
		case recXF:
			if len(rec.data) >= 4 {
				wb.xfFormat = append(wb.xfFormat, le.Uint16(rec.data[2:]))
			}
		case recBoundSheet:
			if len(rec.data) >= 8 {
				// BoundSheet8: lbPlyPos(4) hsState(1) dt(1) + ShortXLUnicodeString (cch, flags, символы)
				r := &chunkReader{chunks: [][]byte{rec.data[6:]}}
				name, _ := r.readString(1)
				wb.sheets = append(wb.sheets, sheetInfo{
					name:   name,
					offset: int(le.Uint32(rec.data)),
					typ:    rec.data[5],
				})
			}
		case recSST:
			wb.sst = parseSST(rec.chunks)
		}
		if depth <= 0 {
			break
		}
	}
	if len(wb.sheets) == 0 {
		return nil, errors.New("no sheets found")
	}

	var out []Sheet
	for _, si := range wb.sheets {
		if si.typ != 0 { // только обычные листы (не диаграммы/макросы)
			continue
		}
		rows := wb.parseSheet(b, si.offset)
		out = append(out, Sheet{Name: si.name, Rows: rows})
	}
	return out, nil
}

func parseSST(chunks [][]byte) []string {
	if len(chunks) == 0 || len(chunks[0]) < 8 {
		return nil
	}
	le := binary.LittleEndian
	unique := int(le.Uint32(chunks[0][4:]))
	first := append([]byte(nil), chunks[0][8:]...)
	r := &chunkReader{chunks: append([][]byte{first}, chunks[1:]...)}
	// Число строк берётся из файла и не заслуживает доверия: ограничиваем его
	// объёмом данных (на строку нужно минимум 3 байта), иначе битый файл
	// вызовет аллокацию на гигабайты и падение процесса по OOM.
	total := 0
	for _, c := range r.chunks {
		total += len(c)
	}
	if maxStrings := total / 3; unique > maxStrings {
		unique = maxStrings
	}
	if unique < 0 {
		unique = 0
	}
	res := make([]string, 0, unique)
	for i := 0; i < unique; i++ {
		s, err := r.readString(2)
		if err != nil {
			break
		}
		res = append(res, s)
	}
	return res
}

func (wb *workbook) isDateXF(xf uint16) bool {
	if int(xf) >= len(wb.xfFormat) {
		return false
	}
	id := wb.xfFormat[xf]
	switch {
	case id >= 14 && id <= 22, id >= 45 && id <= 47:
		return true
	case id < 164:
		return false
	}
	f, ok := wb.formats[id]
	if !ok {
		return false
	}
	inQuote, inBracket := false, false
	for _, c := range strings.ToLower(f) {
		switch {
		case c == '"':
			inQuote = !inQuote
		case inQuote:
		case c == '[':
			inBracket = true
		case c == ']':
			inBracket = false
		case inBracket:
		case c == 'y' || c == 'd' || c == 'h' || c == 's':
			return true
		}
	}
	return false
}

func (wb *workbook) formatDate(v float64) string {
	var base time.Time
	if wb.dateMode == 1 {
		base = time.Date(1904, 1, 1, 0, 0, 0, 0, time.UTC)
	} else {
		base = time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)
	}
	days := math.Floor(v)
	frac := v - days
	t := base.AddDate(0, 0, int(days)).Add(time.Duration(math.Round(frac*86400)) * time.Second)
	switch {
	case days == 0:
		return t.Format("15:04:05")
	case frac == 0:
		return t.Format("2006-01-02")
	}
	return t.Format("2006-01-02 15:04:05")
}

func (wb *workbook) number(v float64, xf uint16) string {
	if wb.isDateXF(xf) {
		return wb.formatDate(v)
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

func decodeRK(rk uint32) float64 {
	var v float64
	if rk&2 != 0 {
		v = float64(int32(rk) >> 2)
	} else {
		v = math.Float64frombits(uint64(rk&0xFFFFFFFC) << 32)
	}
	if rk&1 != 0 {
		v /= 100
	}
	return v
}

var errorCodes = map[byte]string{
	0x00: "#NULL!", 0x07: "#DIV/0!", 0x0F: "#VALUE!", 0x17: "#REF!",
	0x1D: "#NAME?", 0x24: "#NUM!", 0x2A: "#N/A",
}

func (wb *workbook) parseSheet(b []byte, offset int) [][]string {
	le := binary.LittleEndian
	cells := map[[2]int]string{}
	maxRow, maxCol := -1, -1

	set := func(r, c int, v string) {
		cells[[2]int{r, c}] = v
		if r > maxRow {
			maxRow = r
		}
		if c > maxCol {
			maxCol = c
		}
	}

	pendingString := [2]int{-1, -1}
	pos := offset
	depth := 0
	for pos >= 0 {
		rec, next, ok := nextRecord(b, pos)
		if !ok {
			break
		}
		pos = next
		d := rec.data
		switch rec.id {
		case recBOF:
			depth++
		case recEOF:
			depth--
		case recNumber:
			if len(d) >= 14 {
				set(int(le.Uint16(d)), int(le.Uint16(d[2:])),
					wb.number(math.Float64frombits(le.Uint64(d[6:])), le.Uint16(d[4:])))
			}
		case recRK:
			if len(d) >= 10 {
				set(int(le.Uint16(d)), int(le.Uint16(d[2:])),
					wb.number(decodeRK(le.Uint32(d[6:])), le.Uint16(d[4:])))
			}
		case recMulRK:
			if len(d) >= 6 {
				row := int(le.Uint16(d))
				first := int(le.Uint16(d[2:]))
				n := (len(d) - 6) / 6
				for i := 0; i < n; i++ {
					p := 4 + i*6
					set(row, first+i, wb.number(decodeRK(le.Uint32(d[p+2:])), le.Uint16(d[p:])))
				}
			}
		case recLabelSST:
			if len(d) >= 10 {
				idx := int(le.Uint32(d[6:]))
				if idx < len(wb.sst) {
					set(int(le.Uint16(d)), int(le.Uint16(d[2:])), wb.sst[idx])
				}
			}
		case recLabel:
			if len(d) >= 8 {
				r := &chunkReader{chunks: [][]byte{d[6:]}}
				if s, err := r.readString(2); err == nil {
					set(int(le.Uint16(d)), int(le.Uint16(d[2:])), s)
				}
			}
		case recBoolErr:
			if len(d) >= 8 {
				set(int(le.Uint16(d)), int(le.Uint16(d[2:])), boolErr(d[6], d[7]))
			}
		case recFormula:
			if len(d) >= 14 {
				row, col := int(le.Uint16(d)), int(le.Uint16(d[2:]))
				res := d[6:14]
				if res[6] == 0xFF && res[7] == 0xFF {
					switch res[0] {
					case 0: // строка в следующей записи STRING
						pendingString = [2]int{row, col}
						set(row, col, "")
					case 1:
						set(row, col, boolErr(res[2], 0))
					case 2:
						set(row, col, boolErr(res[2], 1))
					default:
						set(row, col, "")
					}
				} else {
					set(row, col, wb.number(math.Float64frombits(le.Uint64(res)), le.Uint16(d[4:])))
				}
			}
		case recString:
			if pendingString[0] >= 0 {
				r := &chunkReader{chunks: rec.chunks}
				if s, err := r.readString(2); err == nil {
					set(pendingString[0], pendingString[1], s)
				}
				pendingString = [2]int{-1, -1}
			}
		}
		if depth <= 0 {
			break
		}
	}

	rows := make([][]string, maxRow+1)
	for i := range rows {
		rows[i] = make([]string, maxCol+1)
	}
	for k, v := range cells {
		rows[k[0]][k[1]] = v
	}
	return rows
}

func boolErr(val, isErr byte) string {
	if isErr != 0 {
		if s, ok := errorCodes[val]; ok {
			return s
		}
		return "#ERR"
	}
	if val != 0 {
		return "TRUE"
	}
	return "FALSE"
}

// ---------------------------------------------------------------------------
// Чтение строк BIFF8 (с поддержкой CONTINUE)
// ---------------------------------------------------------------------------

type chunkReader struct {
	chunks [][]byte
	ci     int
	pos    int
}

var errEOD = errors.New("unexpected end of record data")

func (r *chunkReader) u8() (byte, error) {
	for r.ci < len(r.chunks) && r.pos >= len(r.chunks[r.ci]) {
		r.ci++
		r.pos = 0
	}
	if r.ci >= len(r.chunks) {
		return 0, errEOD
	}
	v := r.chunks[r.ci][r.pos]
	r.pos++
	return v, nil
}

func (r *chunkReader) skip(n int) {
	for ; n > 0; n-- {
		if _, err := r.u8(); err != nil {
			return
		}
	}
}

// readString читает строку BIFF8: длина (1 или 2 байта), флаги, символы.
func (r *chunkReader) readString(lenBytes int) (string, error) {
	b0, err := r.u8()
	if err != nil {
		return "", err
	}
	n := int(b0)
	if lenBytes == 2 {
		b1, err := r.u8()
		if err != nil {
			return "", err
		}
		n |= int(b1) << 8
	}
	flags, err := r.u8()
	if err != nil {
		return "", err
	}
	runs, ext := 0, 0
	if flags&0x08 != 0 {
		a, _ := r.u8()
		b, _ := r.u8()
		runs = int(a) | int(b)<<8
	}
	if flags&0x04 != 0 {
		var v uint32
		for i := 0; i < 4; i++ {
			x, _ := r.u8()
			v |= uint32(x) << (8 * i)
		}
		ext = int(v)
	}

	var sb strings.Builder
	wide := flags&0x01 != 0
	for n > 0 {
		if r.ci >= len(r.chunks) {
			return sb.String(), errEOD
		}
		if r.pos >= len(r.chunks[r.ci]) {
			// переход на CONTINUE: первым байтом идёт новый флаг кодировки
			r.ci++
			r.pos = 0
			if r.ci >= len(r.chunks) {
				return sb.String(), errEOD
			}
			if len(r.chunks[r.ci]) == 0 {
				continue
			}
			f, _ := r.u8()
			wide = f&0x01 != 0
			continue
		}
		cur := r.chunks[r.ci][r.pos:]
		if wide {
			k := len(cur) / 2
			if k == 0 {
				// нечётный остаток — невозможно, пропускаем байт
				r.pos++
				continue
			}
			if k > n {
				k = n
			}
			u := make([]uint16, k)
			for i := range u {
				u[i] = binary.LittleEndian.Uint16(cur[2*i:])
			}
			sb.WriteString(string(utf16.Decode(u)))
			r.pos += 2 * k
			n -= k
		} else {
			k := len(cur)
			if k > n {
				k = n
			}
			for _, c := range cur[:k] {
				sb.WriteRune(rune(c))
			}
			r.pos += k
			n -= k
		}
	}
	r.skip(4*runs + ext)
	return sb.String(), nil
}
