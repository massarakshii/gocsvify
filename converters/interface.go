package converters

type Converter interface {
	Convert(csvPath string) error
}
