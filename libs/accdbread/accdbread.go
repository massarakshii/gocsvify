// Package accdbread читает Microsoft Access (.mdb: Jet3/Jet4, .accdb: ACE) на чистом Go,
// только на стандартной библиотеке, без ODBC/mdbtools и сторонних модулей.
//
// Пример:
//
//	tables, err := accdbread.GetTables("base.accdb")
//	...
//	f, _ := os.Create("clients.csv")
//	err = accdbread.ConvertToCsv("base.accdb", "Клиенты", f, ',', 0)
//
// Ограничения: файлы, зашифрованные паролем/шифрованием Access, не поддерживаются;
// индексы, связи, запросы и формы не разбираются — только таблицы с данными.
// Вычисляемые поля Access 2010+ разбираются для типов Text, Long и Currency, значения
// других типов выводятся как 0x… (шестнадцатеричная строка).
package accdbread

import (
	"crypto/rc4"
	"encoding/binary"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

// ---------- типы колонок ----------

const (
	tBool    = 1
	tByte    = 2
	tInt     = 3
	tLong    = 4
	tMoney   = 5
	tFloat   = 6
	tDouble  = 7
	tDate    = 8
	tBinary  = 9
	tText    = 10
	tOLE     = 11
	tMemo    = 12
	tGUID    = 15
	tNumeric = 16
	tComplex = 18
	tBigInt  = 20
	tExtDate = 21
)

var typeNames = map[byte]string{
	tBool: "Boolean", tByte: "Byte", tInt: "Integer", tLong: "Long", tMoney: "Currency",
	tFloat: "Single", tDouble: "Double", tDate: "DateTime", tBinary: "Binary", tText: "Text",
	tOLE: "OLE", tMemo: "Memo", tGUID: "GUID", tNumeric: "Numeric", tComplex: "Complex",
	tBigInt: "BigInt", tExtDate: "DateTimeExt",
}

// ---------- параметры формата ----------

type fmtInfo struct {
	jet4          bool
	pageSize      int
	rowCountOff   int // смещение числа строк в странице данных
	numRowsOff    int // число строк в TDEF
	numColsOff    int
	numRidxOff    int
	usageMapOff   int
	colsStartOff  int
	ridxEntrySize int
	colEntrySize  int
	colNumOff     int
	colVarOff     int
	colFixedOff   int
	colFlagsOff   int
	colSizeOff    int
	rowColsSize   int // размер поля «число колонок» в начале строки
}

var jet3Fmt = fmtInfo{
	jet4: false, pageSize: 2048, rowCountOff: 0x08, numRowsOff: 12, numColsOff: 25,
	numRidxOff: 31, usageMapOff: 35, colsStartOff: 43, ridxEntrySize: 8, colEntrySize: 18,
	colNumOff: 1, colVarOff: 3, colFixedOff: 14, colFlagsOff: 13, colSizeOff: 16, rowColsSize: 1,
}

var jet4Fmt = fmtInfo{
	jet4: true, pageSize: 4096, rowCountOff: 0x0c, numRowsOff: 16, numColsOff: 45,
	numRidxOff: 51, usageMapOff: 55, colsStartOff: 63, ridxEntrySize: 12, colEntrySize: 25,
	colNumOff: 5, colVarOff: 7, colFixedOff: 21, colFlagsOff: 15, colSizeOff: 23, rowColsSize: 2,
}

// ---------- структуры ----------

type Column struct {
	Name     string
	Type     byte
	Num      int // номер колонки (индекс в маске NULL)
	VarNum   int
	FixedOff int
	Size     int
	Fixed    bool
	Calc     bool // вычисляемое поле Access 2010+: значение обёрнуто в служебный заголовок
	Prec     int
	Scale    int
}

type Table struct {
	Name     string
	TdefPage uint32
	Cols     []*Column
	RowCount int // по данным заголовка таблицы (приблизительно)
	NumVar   int // число колонок переменной длины
	usage    uint32
	Err      error
	System   bool
}

type DB struct {
	f        *os.File
	fi       fmtInfo
	cp       int
	numPages int64
	badRows  int
	badLval  int
}

var errStop = errors.New("stop")

// ---------- утилиты ----------

func le16(b []byte) uint16 { return binary.LittleEndian.Uint16(b) }
func le32(b []byte) uint32 { return binary.LittleEndian.Uint32(b) }
func le64(b []byte) uint64 { return binary.LittleEndian.Uint64(b) }

var cp1251hi = [64]rune{
	0x0402, 0x0403, 0x201A, 0x0453, 0x201E, 0x2026, 0x2020, 0x2021, 0x20AC, 0x2030, 0x0409, 0x2039, 0x040A, 0x040C, 0x040B, 0x040F,
	0x0452, 0x2018, 0x2019, 0x201C, 0x201D, 0x2022, 0x2013, 0x2014, 0x0098, 0x2122, 0x0459, 0x203A, 0x045A, 0x045C, 0x045B, 0x045F,
	0x00A0, 0x040E, 0x045E, 0x0408, 0x00A4, 0x0490, 0x00A6, 0x00A7, 0x0401, 0x00A9, 0x0404, 0x00AB, 0x00AC, 0x00AD, 0x00AE, 0x0407,
	0x00B0, 0x00B1, 0x0406, 0x0456, 0x0491, 0x00B5, 0x00B6, 0x00B7, 0x0451, 0x2116, 0x0454, 0x00BB, 0x0458, 0x0405, 0x0455, 0x0457,
}

var cp1252hi = [32]rune{
	0x20AC, 0x0081, 0x201A, 0x0192, 0x201E, 0x2026, 0x2020, 0x2021, 0x02C6, 0x2030, 0x0160, 0x2039, 0x0152, 0x008D, 0x017D, 0x008F,
	0x0090, 0x2018, 0x2019, 0x201C, 0x201D, 0x2022, 0x2013, 0x2014, 0x02DC, 0x2122, 0x0161, 0x203A, 0x0153, 0x009D, 0x017E, 0x0178,
}

// ErrTableNotFound возвращается, если в базе нет таблицы с указанным именем.
var ErrTableNotFound = errors.New("таблица не найдена")

// GetTables возвращает пользовательские таблицы базы (без системных), отсортированные по имени.
// У таблицы, описание которой не удалось прочитать, заполнено поле Err.
// cp — кодовая страница текста для Jet3/Access 97 (например 1251); 0 — взять из файла.
func GetTables(filePath string, cp ...int) ([]*Table, error) {
	db, err := Open(filePath, firstOr0(cp))
	if err != nil {
		return nil, err
	}
	defer db.Close()
	return db.Tables(false)
}

// ConvertToCsv записывает таблицу tableName (без учёта регистра) в w в формате CSV.
// limit > 0 ограничивает число строк. Если таблицы нет, возвращается ErrTableNotFound.
// cp — как в GetTables. Ничего не печатает в stderr и не завершает программу;
// для доступа к предупреждениям о повреждённых строках используйте Open + DumpCSV + Warnings.
func ConvertToCsv(filePath string, tableName string, w io.Writer, delim rune, limit int, cp ...int) error {
	db, err := Open(filePath, firstOr0(cp))
	if err != nil {
		return err
	}
	defer db.Close()

	tables, err := db.Tables(false)
	if err != nil {
		return err
	}
	for _, t := range tables {
		if strings.EqualFold(t.Name, tableName) {
			return db.DumpCSV(t, w, delim, limit)
		}
	}
	return fmt.Errorf("%w: %q", ErrTableNotFound, tableName)
}

// DumpCSV записывает таблицу t в w в формате CSV (первая строка — имена колонок).
func (db *DB) DumpCSV(t *Table, w io.Writer, delim rune, limit int) error {
	return dumpCSV(db, t, w, delim, limit)
}

// Warnings возвращает число пропущенных повреждённых строк и нечитаемых MEMO/OLE значений
// за всё время работы с db.
func (db *DB) Warnings() (badRows, badLval int) { return db.badRows, db.badLval }

func firstOr0(v []int) int {
	if len(v) > 0 {
		return v[0]
	}
	return 0
}

// decode8 — однобайтовый текст Jet3 в кодовой странице базы (1251, иначе 1252/Latin-1).
func (db *DB) decode8(b []byte) string {
	var sb strings.Builder
	for _, c := range b {
		switch {
		case c < 0x80:
			sb.WriteByte(c)
		case db.cp == 1251:
			if c >= 0xC0 {
				sb.WriteRune(0x410 + rune(c-0xC0))
			} else {
				sb.WriteRune(cp1251hi[c-0x80])
			}
		default:
			if c < 0xA0 {
				sb.WriteRune(cp1252hi[c-0x80])
			} else {
				sb.WriteRune(rune(c))
			}
		}
	}
	return sb.String()
}

// decodeUTF16 — текст Jet4/ACE: UTF-16LE, либо «сжатый» (префикс FF FE).
func decodeUTF16(b []byte) string {
	if len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE {
		return decodeCompressed(b[2:])
	}
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = le16(b[2*i:])
	}
	return string(utf16.Decode(u))
}

func decodeCompressed(b []byte) string {
	var sb strings.Builder
	compressed := true
	for i := 0; i < len(b); {
		if b[i] == 0 { // 0x00 переключает режим
			compressed = !compressed
			i++
			continue
		}
		if compressed {
			sb.WriteRune(rune(b[i]))
			i++
		} else {
			if i+1 >= len(b) {
				break
			}
			sb.WriteRune(rune(le16(b[i:])))
			i += 2
		}
	}
	return sb.String()
}

func (db *DB) decodeText(b []byte) string {
	if db.fi.jet4 {
		return decodeUTF16(b)
	}
	return db.decode8(b)
}

// ---------- открытие файла ----------

func Open(path string, cpOverride int) (*DB, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	hdr := make([]byte, 0x100)
	if n, err := f.ReadAt(hdr, 0); n < len(hdr) {
		f.Close()
		if err == nil || err == io.EOF {
			err = errors.New("файл слишком короткий")
		}
		return nil, err
	}
	sig := string(hdr[4:19])
	if hdr[0] != 0 || hdr[1] != 1 || (sig != "Standard Jet DB" && sig != "Standard ACE DB") {
		f.Close()
		return nil, errors.New("это не файл Microsoft Access (.mdb/.accdb)")
	}
	// Версия формата: 0 — Jet3 (Access 97), 1 — Jet4 (2000–2003), 2 — ACE 2007,
	// 3 — ACE 2010 и новее. Старшие байты слова у новых версий (0x0103, 0x0603 …)
	// несут подверсию и на разбор страниц не влияют, поэтому смотрим на младший байт.
	ver := le32(hdr[0x14:])
	db := &DB{f: f}
	switch v := ver & 0xFF; {
	case v == 0:
		db.fi = jet3Fmt
	case v <= 0x0F:
		db.fi = jet4Fmt
	default:
		f.Close()
		return nil, fmt.Errorf("неизвестная версия формата: 0x%X", ver)
	}
	// Заголовок (с 0x18) зашифрован RC4 с постоянным ключом 0x6b39dac7.
	n := 128
	if !db.fi.jet4 {
		n = 126
	}
	if c, err := rc4.NewCipher([]byte{0xC7, 0xDA, 0x39, 0x6B}); err == nil {
		c.XORKeyStream(hdr[0x18:0x18+n], hdr[0x18:0x18+n])
	}
	db.cp = int(le16(hdr[0x3C:]))
	if cpOverride != 0 {
		db.cp = cpOverride
	}
	db.numPages = st.Size() / int64(db.fi.pageSize)
	return db, nil
}

func (db *DB) Close() error { return db.f.Close() }

func (db *DB) page(n uint32) ([]byte, error) {
	if int64(n) >= db.numPages {
		return nil, fmt.Errorf("страница %d за пределами файла", n)
	}
	buf := make([]byte, db.fi.pageSize)
	if _, err := db.f.ReadAt(buf, int64(n)*int64(db.fi.pageSize)); err != nil {
		return nil, err
	}
	return buf, nil
}

// ---------- строки на странице ----------

type slot struct {
	start, end          int
	deleted, lookup, ok bool
}

func (db *DB) slots(pg []byte) []slot {
	fi := db.fi
	cnt := int(le16(pg[fi.rowCountOff:]))
	base := fi.rowCountOff + 2
	if max := (len(pg) - base) / 2; cnt > max {
		cnt = max
	}
	res := make([]slot, cnt)
	prev := len(pg)
	for i := 0; i < cnt; i++ {
		raw := int(le16(pg[base+2*i:]))
		s := slot{start: raw & 0x1FFF, end: prev, deleted: raw&0x8000 != 0, lookup: raw&0x4000 != 0}
		s.ok = s.start >= base && s.start <= s.end && s.end <= len(pg)
		res[i] = s
		prev = s.start
	}
	return res
}

// rowAt читает строку по указателю «страница<<8 | номер строки».
func (db *DB) rowAt(ptr uint32) ([]byte, error) {
	pg, err := db.page(ptr >> 8)
	if err != nil {
		return nil, err
	}
	sl := db.slots(pg)
	r := int(ptr & 0xFF)
	if r >= len(sl) || !sl[r].ok {
		return nil, fmt.Errorf("нет строки %d на странице %d", r, ptr>>8)
	}
	return pg[sl[r].start:sl[r].end], nil
}

// ---------- определение таблицы ----------

func (db *DB) loadTable(pgNo uint32) (*Table, error) {
	pg, err := db.page(pgNo)
	if err != nil {
		return nil, err
	}
	if pg[0] != 0x02 {
		return nil, fmt.Errorf("страница %d не является описанием таблицы (тип 0x%02X)", pgNo, pg[0])
	}
	buf := append([]byte(nil), pg...)
	next := le32(pg[4:])
	for guard := 0; next != 0 && guard < 10000; guard++ {
		p, err := db.page(next)
		if err != nil {
			return nil, err
		}
		buf = append(buf, p[8:]...)
		next = le32(p[4:])
	}
	fi := db.fi
	if len(buf) < fi.colsStartOff {
		return nil, errors.New("слишком короткое описание таблицы")
	}
	t := &Table{TdefPage: pgNo}
	t.RowCount = int(le32(buf[fi.numRowsOff:]))
	numCols := int(le16(buf[fi.numColsOff:]))
	t.NumVar = int(le16(buf[fi.numColsOff-2:]))
	numRidx := int(le32(buf[fi.numRidxOff:]))
	t.usage = le32(buf[fi.usageMapOff:])
	if numCols <= 0 || numCols > 4096 || numRidx < 0 || numRidx > 4096 {
		return nil, errors.New("некорректное описание таблицы (шифрование или повреждение?)")
	}
	cur := fi.colsStartOff + fi.ridxEntrySize*numRidx
	cols := make([]*Column, 0, numCols)
	for i := 0; i < numCols; i++ {
		if cur+fi.colEntrySize > len(buf) {
			return nil, errors.New("описание колонок выходит за пределы")
		}
		e := buf[cur : cur+fi.colEntrySize]
		cols = append(cols, &Column{
			Type:     e[0],
			Num:      int(le16(e[fi.colNumOff:])),
			VarNum:   int(le16(e[fi.colVarOff:])),
			FixedOff: int(le16(e[fi.colFixedOff:])),
			Size:     int(le16(e[fi.colSizeOff:])),
			Fixed:    e[fi.colFlagsOff]&0x01 != 0,
			Calc:     fi.jet4 && e[fi.colFlagsOff+1]&0xC0 != 0,
			Prec:     int(e[11]),
			Scale:    int(e[12]),
		})
		cur += fi.colEntrySize
	}
	for _, c := range cols {
		var n int
		if fi.jet4 {
			if cur+2 > len(buf) {
				return nil, errors.New("имена колонок выходят за пределы")
			}
			n = int(le16(buf[cur:]))
			cur += 2
		} else {
			if cur+1 > len(buf) {
				return nil, errors.New("имена колонок выходят за пределы")
			}
			n = int(buf[cur])
			cur++
		}
		if cur+n > len(buf) {
			return nil, errors.New("имя колонки выходит за пределы")
		}
		c.Name = db.decodeText(buf[cur : cur+n])
		cur += n
	}
	sort.SliceStable(cols, func(i, j int) bool { return cols[i].Num < cols[j].Num })
	t.Cols = cols
	return t, nil
}

// ---------- карта использования страниц ----------

func (db *DB) usagePages(ptr uint32) ([]uint32, error) {
	buf, err := db.rowAt(ptr)
	if err != nil {
		return nil, err
	}
	if len(buf) < 5 {
		return nil, errors.New("слишком короткая карта страниц")
	}
	var pages []uint32
	switch buf[0] {
	case 0: // встроенная карта: стартовая страница + битовая маска
		start := le32(buf[1:])
		for i, b := range buf[5:] {
			for j := 0; j < 8; j++ {
				if b&(1<<uint(j)) != 0 {
					pages = append(pages, start+uint32(i*8+j))
				}
			}
		}
	case 1: // карта ссылок на страницы с битовыми масками
		per := (db.fi.pageSize - 4) * 8
		for i := 0; 1+4*i+4 <= len(buf); i++ {
			mp := le32(buf[1+4*i:])
			if mp == 0 {
				continue
			}
			p, err := db.page(mp)
			if err != nil {
				continue
			}
			for k, b := range p[4:] {
				if b == 0 {
					continue
				}
				for j := 0; j < 8; j++ {
					if b&(1<<uint(j)) != 0 {
						pages = append(pages, uint32(i*per+k*8+j))
					}
				}
			}
		}
	default:
		return nil, fmt.Errorf("неизвестный тип карты страниц %d", buf[0])
	}
	return pages, nil
}

func (db *DB) tablePages(t *Table) []uint32 {
	if t.usage != 0 {
		if ps, err := db.usagePages(t.usage); err == nil {
			return ps
		}
	}
	// запасной вариант: просмотр всего файла в поисках страниц данных этой таблицы
	var res []uint32
	for n := int64(1); n < db.numPages; n++ {
		pg, err := db.page(uint32(n))
		if err == nil && pg[0] == 0x01 && le32(pg[4:]) == t.TdefPage {
			res = append(res, uint32(n))
		}
	}
	return res
}

// ---------- чтение строк ----------

func (db *DB) ForEachRow(t *Table, fn func([]any) error) error {
	pages := db.tablePages(t)
	isData := func(pg []byte) bool { return pg[0] == 0x01 && le32(pg[4:]) == t.TdefPage }

	// Проход 1: собираем цели указателей «переполненных» строк, чтобы не выдать их дважды.
	targets := map[uint32]bool{}
	for _, p := range pages {
		pg, err := db.page(p)
		if err != nil || !isData(pg) {
			continue
		}
		for _, s := range db.slots(pg) {
			// Указатель на «переполненную» строку: 4 байта в начале слота, но сам слот
			// может быть длиннее — остаётся место, выделенное под прежнюю строку.
			if s.ok && !s.deleted && s.lookup && s.end-s.start >= 4 {
				targets[le32(pg[s.start:])] = true
			}
		}
	}
	// Проход 2: сами строки.
	for _, p := range pages {
		pg, err := db.page(p)
		if err != nil || !isData(pg) {
			continue
		}
		for i, s := range db.slots(pg) {
			if !s.ok || s.deleted {
				continue
			}
			var row []byte
			if s.lookup {
				if s.end-s.start < 4 {
					continue // слишком короткий слот для указателя
				}
				r, err := db.rowAt(le32(pg[s.start:]))
				if err != nil {
					db.badRows++
					continue
				}
				row = r
			} else {
				if targets[p<<8|uint32(i)] {
					continue
				}
				row = pg[s.start:s.end]
			}
			vals, err := db.decodeRow(t, row)
			if err != nil {
				db.badRows++
				continue
			}
			if err := fn(vals); err != nil {
				return err
			}
		}
	}
	return nil
}

func (db *DB) decodeRow(t *Table, row []byte) (vals []any, err error) {
	defer func() {
		if r := recover(); r != nil {
			vals, err = nil, fmt.Errorf("повреждённая строка: %v", r)
		}
	}()
	fi := db.fi
	n := len(row)
	var colCount int
	if fi.jet4 {
		colCount = int(le16(row))
	} else {
		colCount = int(row[0])
	}
	nmask := (colCount + 7) / 8
	if n < fi.rowColsSize+nmask+1 {
		return nil, errors.New("строка слишком короткая")
	}
	mask := row[n-nmask:]

	var varCount int
	var varOff []int
	if t.NumVar == 0 {
		// В таблице нет полей переменной длины: счётчик и таблица смещений в строке
		// отсутствуют, после данных сразу идёт маска NULL.
	} else if fi.jet4 {
		varCount = int(le16(row[n-nmask-2:]))
		varOff = make([]int, varCount+1)
		for i := 0; i <= varCount; i++ {
			p := n - nmask - 4 - 2*i
			if p < 0 {
				return nil, errors.New("таблица смещений за пределами строки")
			}
			varOff[i] = int(le16(row[p:]))
		}
	} else {
		varCount = int(row[n-1-nmask])
		varOff = make([]int, varCount+1)
		numJumps := (n - 1) / 256
		colPtr := n - 1 - nmask - numJumps - 1
		if (colPtr-varCount)/256 < numJumps {
			numJumps--
		}
		jumpsUsed := 0
		for i := 0; i <= varCount; i++ {
			for jumpsUsed < numJumps && i == int(row[n-1-nmask-jumpsUsed-1]) {
				jumpsUsed++
			}
			if colPtr-i < 0 {
				return nil, errors.New("таблица смещений за пределами строки")
			}
			varOff[i] = int(row[colPtr-i]) + jumpsUsed*256
		}
	}

	vals = make([]any, len(t.Cols))
	for i, c := range t.Cols {
		notNull := false
		if c.Num < colCount {
			notNull = mask[c.Num/8]&(1<<uint(c.Num%8)) != 0
		}
		if c.Type == tBool {
			vals[i] = notNull
			continue
		}
		if !notNull {
			continue
		}
		var raw []byte
		if c.Fixed {
			s := fi.rowColsSize + c.FixedOff
			if s < 0 || s+c.Size > n {
				continue
			}
			raw = row[s : s+c.Size]
		} else {
			if c.VarNum >= varCount {
				continue
			}
			s, e := varOff[c.VarNum], varOff[c.VarNum+1]
			if s < 0 || s > e || e > n {
				continue
			}
			raw = row[s:e]
		}
		if c.Calc {
			// Вычисляемое поле: 16 байт служебных данных, 4 байта длины значения,
			// само значение, затем добивка и 3 байта хвоста.
			if len(raw) < 20 {
				continue
			}
			dl := int(le32(raw[16:]))
			if dl < 0 || 20+dl > len(raw) {
				continue
			}
			raw = raw[20 : 20+dl]
			// Тип в описании вычисляемой колонки не всегда совпадает с типом результата
			// выражения (например, «Double» с денежным значением внутри), поэтому
			// проверены и разбираются только Text, Long и Currency; остальное — как есть,
			// шестнадцатеричной строкой, чтобы не выдавать правдоподобные, но неверные числа.
			if !(c.Type == tText || (c.Type == tLong && len(raw) == 4) || (c.Type == tMoney && len(raw) == 8)) {
				vals[i] = append([]byte(nil), raw...)
				continue
			}
		}
		vals[i] = db.convert(c, raw)
	}
	return vals, nil
}

// ---------- преобразование значений ----------

func (db *DB) convert(c *Column, raw []byte) any {
	need := func(k int) bool { return len(raw) >= k }
	switch c.Type {
	case tByte:
		if need(1) {
			return int64(raw[0])
		}
	case tInt:
		if need(2) {
			return int64(int16(le16(raw)))
		}
	case tLong, tComplex:
		if need(4) {
			return int64(int32(le32(raw)))
		}
	case tBigInt:
		if need(8) {
			return int64(le64(raw))
		}
	case tMoney:
		if need(8) {
			v := int64(le64(raw))
			neg := v < 0
			a := uint64(v)
			if neg {
				a = uint64(-v)
			}
			s := fmt.Sprintf("%d.%04d", a/10000, a%10000)
			if neg {
				s = "-" + s
			}
			return s
		}
	case tFloat:
		if need(4) {
			return math.Float32frombits(le32(raw))
		}
	case tDouble:
		if need(8) {
			return math.Float64frombits(le64(raw))
		}
	case tDate:
		if need(8) {
			return dateString(math.Float64frombits(le64(raw)))
		}
	case tText:
		return db.decodeText(raw)
	case tMemo:
		data, err := db.readLval(raw)
		if err != nil {
			db.badLval++
			return nil
		}
		return db.decodeText(data)
	case tOLE:
		data, err := db.readLval(raw)
		if err != nil {
			db.badLval++
			return nil
		}
		return data
	case tGUID:
		if need(16) {
			return fmt.Sprintf("{%08X-%04X-%04X-%X-%X}", le32(raw), le16(raw[4:]), le16(raw[6:]), raw[8:10], raw[10:16])
		}
	case tNumeric:
		if need(17) {
			return numericString(raw, c.Scale)
		}
	case tExtDate:
		return strings.TrimRight(string(raw), "\x00")
	}
	return append([]byte(nil), raw...)
}

func dateString(d float64) any {
	if math.IsNaN(d) || math.IsInf(d, 0) || math.Abs(d) > 3e6 {
		return nil
	}
	whole := math.Trunc(d)
	frac := math.Abs(d - whole)
	sec := int64(math.Round(frac * 86400))
	t := time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC).AddDate(0, 0, int(whole)).Add(time.Duration(sec) * time.Second)
	return t.Format("2006-01-02 15:04:05")
}

func numericString(raw []byte, scale int) string {
	neg := raw[0]&0x80 != 0
	b := make([]byte, 16)
	for g := 0; g < 4; g++ { // 4 группы по 4 байта, в каждой порядок little-endian
		for k := 0; k < 4; k++ {
			b[g*4+k] = raw[1+g*4+3-k]
		}
	}
	n := new(big.Int).SetBytes(b)
	s := n.String()
	if scale > 0 {
		if len(s) <= scale {
			s = strings.Repeat("0", scale-len(s)+1) + s
		}
		s = s[:len(s)-scale] + "." + s[len(s)-scale:]
	}
	if neg && n.Sign() != 0 {
		s = "-" + s
	}
	return s
}

// readLval читает длинные значения (MEMO / OLE).
func (db *DB) readLval(raw []byte) ([]byte, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if len(raw) < 12 {
		return nil, errors.New("короткий заголовок LVAL")
	}
	hdr := le32(raw)
	length := int(hdr & 0x3FFFFFFF)
	trim := func(b []byte) []byte {
		if length < len(b) {
			b = b[:length]
		}
		return append([]byte(nil), b...)
	}
	switch {
	case hdr&0x80000000 != 0: // данные прямо в строке
		return trim(raw[12:]), nil
	case hdr&0x40000000 != 0: // данные в одной строке другой страницы
		r, err := db.rowAt(le32(raw[4:]))
		if err != nil {
			return nil, err
		}
		return trim(r), nil
	default: // цепочка строк: [следующий указатель(4)] + данные
		var out []byte
		ptr := le32(raw[4:])
		for guard := 0; ptr != 0 && guard < 1<<20; guard++ {
			r, err := db.rowAt(ptr)
			if err != nil {
				return nil, err
			}
			if len(r) < 4 {
				break
			}
			ptr = le32(r)
			out = append(out, r[4:]...)
		}
		return trim(out), nil
	}
}

// ---------- каталог таблиц ----------

func (db *DB) Tables(includeSys bool) ([]*Table, error) {
	cat, err := db.loadTable(2)
	if err != nil {
		return nil, fmt.Errorf("не удалось прочитать системный каталог (файл зашифрован, защищён или повреждён?): %w", err)
	}
	cat.Name = "MSysObjects"
	idx := map[string]int{}
	for i, c := range cat.Cols {
		idx[strings.ToLower(c.Name)] = i
	}
	for _, k := range []string{"id", "name", "type", "flags"} {
		if _, ok := idx[k]; !ok {
			return nil, fmt.Errorf("в каталоге нет колонки %q", k)
		}
	}
	var res []*Table
	seen := map[uint32]bool{}
	err = db.ForEachRow(cat, func(v []any) error {
		name, _ := v[idx["name"]].(string)
		typ, _ := v[idx["type"]].(int64)
		id, _ := v[idx["id"]].(int64)
		flags, _ := v[idx["flags"]].(int64)
		if typ != 1 || name == "" {
			return nil
		}
		sys := uint32(flags)&0x80000002 != 0
		if sys && !includeSys {
			return nil
		}
		tdef := uint32(id) & 0xFFFFFF
		if seen[tdef] {
			return nil
		}
		seen[tdef] = true
		t, err := db.loadTable(tdef)
		if err != nil {
			t = &Table{TdefPage: tdef, Err: err}
		}
		t.Name = name
		t.System = sys
		res = append(res, t)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(res, func(i, j int) bool { return strings.ToLower(res[i].Name) < strings.ToLower(res[j].Name) })
	return res, nil
}

// ---------- вывод ----------

func fmtVal(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case bool:
		if x {
			return "true"
		}
		return "false"
	case int64:
		return strconv.FormatInt(x, 10)
	case float32:
		return strconv.FormatFloat(float64(x), 'g', -1, 32)
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	case string:
		return x
	case []byte:
		return "0x" + hex.EncodeToString(x)
	}
	return fmt.Sprint(v)
}

func dumpCSV(db *DB, t *Table, w io.Writer, delim rune, limit int) error {
	if t.Err != nil {
		return fmt.Errorf("таблица %q: %w", t.Name, t.Err)
	}
	cw := csv.NewWriter(w)
	cw.Comma = delim
	hdr := make([]string, len(t.Cols))
	for i, c := range t.Cols {
		hdr[i] = c.Name
	}
	if err := cw.Write(hdr); err != nil {
		return err
	}
	count := 0
	err := db.ForEachRow(t, func(v []any) error {
		rec := make([]string, len(v))
		for i, x := range v {
			rec[i] = fmtVal(x)
		}
		if err := cw.Write(rec); err != nil {
			return err
		}
		count++
		if limit > 0 && count >= limit {
			return errStop
		}
		return nil
	})
	if errors.Is(err, errStop) {
		err = nil
	}
	cw.Flush()
	if err == nil {
		err = cw.Error()
	}
	return err
}
