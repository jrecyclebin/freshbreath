package server

import "testing"

func TestTaskArgValue(t *testing.T) {
	cases := []struct {
		name string
		v    interface{}
		want string
	}{
		{"string", "hello", "hello"},
		{"string with spaces", "hello world", "hello world"},
		{"float64 whole", float64(42), "42"},
		{"float64 decimal", float64(3.14), "3.14"},
		{"int", int(7), "7"},
		{"int64", int64(99), "99"},
		{"bool true", true, "true"},
		{"bool false", false, "false"},
		{"array", []interface{}{"a", "b"}, `["a","b"]`},
		{"map", map[string]interface{}{"k": "v"}, `{"k":"v"}`},
		{"nil", nil, "null"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := taskArgValue(tc.v)
			if got != tc.want {
				t.Errorf("taskArgValue(%#v) = %q, want %q", tc.v, got, tc.want)
			}
		})
	}
}
