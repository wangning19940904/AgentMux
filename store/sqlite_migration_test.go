package store

import "testing"

func TestNormalizeSQLiteMigrationBoolean(t *testing.T) {
	for _, test := range []struct {
		name  string
		input any
		want  bool
	}{
		{name: "sqlite true integer", input: int64(1), want: true},
		{name: "sqlite false integer", input: int64(0), want: false},
		{name: "text true", input: "true", want: true},
		{name: "text false", input: []byte("0"), want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := normalizeSQLiteMigrationValue("boolean", test.input)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("normalized value = %#v, want %v", got, test.want)
			}
		})
	}
}

func TestNormalizeSQLiteMigrationBooleanRejectsInvalidText(t *testing.T) {
	if _, err := normalizeSQLiteMigrationValue("boolean", "maybe"); err == nil {
		t.Fatal("expected invalid boolean error")
	}
}
