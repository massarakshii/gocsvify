package configTime

import (
	"encoding/json/v2"
	"time"
)

type Duration time.Duration

func (d *Duration) UnmarshalJSON(data []byte) error {
	if len(data) > 0 && (data[0] >= '0' && data[0] <= '9') {
		var num int64
		if err := json.Unmarshal(data, &num); err != nil {
			return err
		}
		*d = Duration(time.Duration(num) * time.Second)
		return nil
	}

	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(v)

	return nil
}

func (d *Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(*d).String())
}
