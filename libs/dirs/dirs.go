package dirs

import "os"

/*func CreateDirs(dirs []string) error {
	for _, path := range dirs {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			if err := os.MkdirAll(path, 0755); err != nil {
				return err
			}
		}
	}
	return nil
}*/

func CreateDirs(dirs []string) error {
	for _, path := range dirs {
		if err := os.MkdirAll(path, 0o755); err != nil {
			return err
		}
	}
	return nil
}
