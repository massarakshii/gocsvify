package zipper

import (
	"archive/zip"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

func ZipFiles(zipFilePath string, filesPath string) (err error) {
	outFile, err := os.Create(zipFilePath)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := outFile.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			if removeErr := os.Remove(zipFilePath); removeErr != nil {
				slog.Error("Failed to remove the archive:", slog.Group("details", "error", removeErr))
			}
		}
	}()

	zipWriter := zip.NewWriter(outFile)
	defer func() {
		if closeErr := zipWriter.Close(); err == nil {
			err = closeErr
		}
	}()

	entries, err := os.ReadDir(filesPath)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}

		if entry.IsDir() {
			continue
		}

		filePath := filepath.Join(filesPath, entry.Name())
		err = addFileToZip(zipWriter, filePath)
		if err != nil {
			return err
		}
	}

	return nil
}

func addFileToZip(writer *zip.Writer, filePath string) error {
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		return err
	}

	header, _ := zip.FileInfoHeader(fileInfo)
	header.Method = zip.Deflate
	header.Name = fileInfo.Name()
	writerFile, err := writer.CreateHeader(header)
	if err != nil {
		return err
	}

	fileToPack, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer fileToPack.Close()

	_, err = io.Copy(writerFile, fileToPack)
	if err != nil {
		return err
	}

	return nil
}
