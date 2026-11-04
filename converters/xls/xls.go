package xls

import (
	"context"
	"encoding/csv"
	"fmt"
	"gocsvify/libs/xlslib"
	"gocsvify/settings"
	"os"
	"path/filepath"
	"strconv"
	// зависит от libxls
)

const defaultDelimiter = ";"

type Converter struct {
	Ctx      context.Context
	FilePath string
	Settings *settings.Settings
}

func NewConverter(ctx context.Context, filePath string, settings *settings.Settings) *Converter {
	return &Converter{
		Ctx:      ctx,
		FilePath: filePath,
		Settings: settings,
	}
}

func (u *Converter) Convert(csvPath string) error {
	f, err := os.Open(u.FilePath)
	if err != nil {
		return err
	}
	defer f.Close()

	sheets, err := xlslib.ReadSheets(f)
	if err != nil {
		return fmt.Errorf("xls %q: %w", u.FilePath, err)
	}

	for i, sh := range sheets {
		if u.Ctx.Err() != nil {
			return u.Ctx.Err()
		}

		if err := u.saveSheet(sh.Rows, i, csvPath); err != nil {
			return err
		}
	}
	return nil
}

func (u *Converter) saveSheet(records [][]string, sheetNum int, csvPath string) error {
	// Создаем CSV-файл
	outFile, err := os.Create(filepath.Join(csvPath, strconv.Itoa(sheetNum)+".csv"))
	if err != nil {
		return err
	}
	defer outFile.Close()

	writer := csv.NewWriter(outFile)
	delimiter := u.Settings.Delimiter
	if delimiter == "" {
		delimiter = defaultDelimiter
	}
	writer.Comma = []rune(delimiter)[0]
	defer writer.Flush()

	if err := writer.Error(); err != nil {
		return err
	}

	// Записываем данные
	for _, record := range records {
		if err := writer.Write(record); err != nil {
			return err
		}
	}

	return nil
}
