package configTime

import (
	"encoding/json/v2"
	"testing"
	"time"
)

func TestDurationUnmarshal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{`10`, 10 * time.Second, false},
		{`0`, 0, false},
		{`"1m30s"`, 90 * time.Second, false},
		{`"250ms"`, 250 * time.Millisecond, false},
		{`"0s"`, 0, false},
		{`"-5s"`, -5 * time.Second, false},
		{`"abc"`, 0, true},
		{`""`, 0, true},
		{`"10"`, 0, true}, // строка без единиц измерения
		{`true`, 0, true},
		{`null`, 0, true},
		{`[1]`, 0, true},
		{`1.5`, 0, true}, // дробное число секунд не поддерживается
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			var d Duration
			err := json.Unmarshal([]byte(tt.in), &d)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && time.Duration(d) != tt.want {
				t.Errorf("got %v, want %v", time.Duration(d), tt.want)
			}
		})
	}
}

func TestDurationMarshalRoundTrip(t *testing.T) {
	t.Parallel()

	type wrap struct {
		D Duration `json:"d"`
	}
	for _, d := range []time.Duration{0, time.Second, 90 * time.Second, time.Hour + 5*time.Millisecond} {
		// Маршалим и значение, и указатель: результат должен быть строкой, а не наносекундами.
		for _, v := range []any{wrap{Duration(d)}, &wrap{Duration(d)}} {
			data, err := json.Marshal(v)
			if err != nil {
				t.Fatalf("Marshal(%v): %v", d, err)
			}
			var back wrap
			if err := json.Unmarshal(data, &back); err != nil {
				t.Fatalf("Unmarshal(%s): %v", data, err)
			}
			if time.Duration(back.D) != d {
				t.Errorf("round trip %v -> %s -> %v", d, data, time.Duration(back.D))
			}
		}
	}
}
