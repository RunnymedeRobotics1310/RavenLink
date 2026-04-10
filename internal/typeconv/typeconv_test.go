package typeconv

import "testing"

func TestToInt(t *testing.T) {
	cases := []struct {
		name  string
		input any
		want  int
		ok    bool
	}{
		{"int", int(42), 42, true},
		{"int64", int64(42), 42, true},
		{"uint32", uint32(42), 42, true},
		{"float64 exact", float64(42), 42, true},
		{"float64 round up", float64(42.6), 43, true},
		{"float64 round down", float64(42.4), 42, true},
		{"float64 negative round", float64(-42.6), -43, true},
		{"float32", float32(3.14), 3, true},
		{"string not numeric", "42", 0, false},
		{"nil", nil, 0, false},
		{"bool", true, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := ToInt(c.input)
			if got != c.want || ok != c.ok {
				t.Errorf("ToInt(%v) = (%d, %v), want (%d, %v)", c.input, got, ok, c.want, c.ok)
			}
		})
	}
}

func TestToInt64(t *testing.T) {
	if v, ok := ToInt64(int64(1 << 40)); v != 1<<40 || !ok {
		t.Errorf("ToInt64(1<<40) = (%d, %v)", v, ok)
	}
	if v, ok := ToInt64(float64(1e12)); v != 1e12 || !ok {
		t.Errorf("ToInt64(1e12) = (%d, %v)", v, ok)
	}
	if _, ok := ToInt64("nope"); ok {
		t.Error("ToInt64(string) should fail")
	}
}
