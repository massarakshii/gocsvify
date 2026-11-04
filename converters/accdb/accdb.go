package accdb

import (
	"bytes"
	"context"
	"gocsvify/libs/accdbread"
	"gocsvify/settings"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const defaultDelimiter = ";"

type Converter struct {
	Ctx      context.Context
	FilePath string
	Settings *settings.Settings
}

func NewConverter(ctx context.Context, filepath string, settings *settings.Settings) *Converter {
	return &Converter{
		Ctx:      ctx,
		FilePath: filepath,
		Settings: settings,
	}
}

func (c *Converter) Convert(csvPath string) error {
	lists, err := c.getLists()
	if err != nil {
		return err
	}

	for i, listName := range lists {
		listName = strings.TrimSpace(listName)
		if listName == "" {
			continue
		}

		err = c.saveList(listName, i, csvPath)
		if err != nil {
			return err
		}
	}

	return nil
}

func (c *Converter) saveList(listName string, listNum int, csvPath string) error {
	_, err := exec.LookPath("mdb-export")
	existMdb := true
	if err != nil {
		existMdb = false
	}

	outFile, err := os.Create(filepath.Join(csvPath, strconv.Itoa(listNum)+".csv"))
	if err != nil {
		return err
	}
	defer outFile.Close()

	delimiter := c.Settings.Delimiter
	if delimiter == "" {
		delimiter = defaultDelimiter
	}
	if !c.Settings.UseMdbLib || !existMdb {
		err = accdbread.ConvertToCsv(c.FilePath, listName, outFile, []rune(delimiter)[0], 0)
		if err != nil {
			return err
		}
	} else {
		cmdExport := exec.CommandContext(c.Ctx, "mdb-export", "-d", delimiter, c.FilePath, listName)
		cmdExport.Stdout = outFile

		err = cmdExport.Run()
		if err != nil {
			return err
		}
	}

	return nil
}

func (c *Converter) getLists() ([]string, error) {
	_, err := exec.LookPath("mdb-tables")
	existMdb := true
	if err != nil {
		existMdb = false
	}

	var lists []string
	if !c.Settings.UseMdbLib || !existMdb {
		tables, err := accdbread.GetTables(c.FilePath)
		if err != nil {
			return nil, err
		}
		for _, table := range tables {
			lists = append(lists, table.Name)
		}
	} else {
		cmdLists := exec.CommandContext(c.Ctx, "mdb-tables", "-1", c.FilePath)
		var outLists bytes.Buffer
		cmdLists.Stdout = &outLists

		err = cmdLists.Run()
		if err != nil {
			return nil, err
		}

		lists = strings.Split(strings.TrimSpace(outLists.String()), "\n")
	}

	return lists, nil
}
