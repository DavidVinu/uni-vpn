package pyjson

import (
	"reflect"
	"testing"
)

// Expected values are what CPython 3.11/3.12 print for json.dumps and json.loads.
func TestFloatRepr(t *testing.T) {
	cases := map[float64]string{
		1759651200.1234567: "1759651200.1234567", 0.1: "0.1", 1e16: "1e+16", 1.5e-05: "1.5e-05",
		123456789012345.6: "123456789012345.6", 15: "15.0", 1e300: "1e+300", 2.5e-300: "2.5e-300",
		0.0001: "0.0001", 1234567890123456.0: "1234567890123456.0",
	}
	for f, want := range cases {
		if got := FloatRepr(f); got != want {
			t.Errorf("%v: %s, want %s", f, got, want)
		}
	}
}

func TestMarshalLikeJSONDumps(t *testing.T) {
	v := O("a", "ä€😀\x7f\x01\"\\/", "b", []any{1, nil, true}, "c", Object{})
	want := `{"a": "\u00e4\u20ac\ud83d\ude00\u007f\u0001\"\\/", "b": [1, null, true], "c": {}}`
	if got := string(Marshal(v)); got != want {
		t.Fatalf("%s\n%s", got, want)
	}
	type tagged struct {
		Name string   `json:"name"`
		List []string `json:"list"`
	}
	if got := string(Marshal(tagged{"x", []string{"y"}})); got != `{"name": "x", "list": ["y"]}` {
		t.Fatal(got)
	}
}

func TestDuplicateKeysKeepTheFirstPositionAndTheLastValue(t *testing.T) {
	v, err := UnmarshalString(`{"a":1,"b":2,"a":3}`)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(v, O("a", Number("3"), "b", Number("2"))) {
		t.Fatal(v)
	}
}

func TestValues(t *testing.T) {
	v, err := UnmarshalString(` {"s": "aä😀\n", "n": [-1.5e3, 0, NaN, -Infinity], "x": null, "t": true} `)
	if err != nil {
		t.Fatal(err)
	}
	want := O("s", "aä😀\n", "n", []any{Number("-1.5e3"), Number("0"), NaN, NegInfinity}, "x", nil, "t", true)
	if !reflect.DeepEqual(v, want) {
		t.Fatalf("%#v", v)
	}
}

func TestErrorsLikePython(t *testing.T) {
	cases := map[string]string{
		`{"a":1,}`:           "Expecting property name enclosed in double quotes: line 1 column 8 (char 7)",
		`[1,]`:               "Expecting value: line 1 column 4 (char 3)",
		`{`:                  "Expecting property name enclosed in double quotes: line 1 column 2 (char 1)",
		`[`:                  "Expecting value: line 1 column 2 (char 1)",
		`"abc`:               "Unterminated string starting at: line 1 column 1 (char 0)",
		`{"a" 1}`:            "Expecting ':' delimiter: line 1 column 6 (char 5)",
		`{"a":1 "b"}`:        "Expecting ',' delimiter: line 1 column 8 (char 7)",
		`tru`:                "Expecting value: line 1 column 1 (char 0)",
		`-`:                  "Expecting value: line 1 column 1 (char 0)",
		`01`:                 "Extra data: line 1 column 2 (char 1)",
		`1.`:                 "Extra data: line 1 column 2 (char 1)",
		`1e`:                 "Extra data: line 1 column 2 (char 1)",
		`"\x"`:               "Invalid \\escape: line 1 column 2 (char 1)",
		`"\u12"`:             "Invalid \\uXXXX escape: line 1 column 3 (char 2)",
		`"\u12g4"`:           "Invalid \\uXXXX escape: line 1 column 3 (char 2)",
		"\"a\x01\"":          "Invalid control character at: line 1 column 3 (char 2)",
		"\ufeff{}":           "Unexpected UTF-8 BOM (decode using utf-8-sig): line 1 column 1 (char 0)",
		`{1:2}`:              "Expecting property name enclosed in double quotes: line 1 column 2 (char 1)",
		`[1 2]`:              "Expecting ',' delimiter: line 1 column 4 (char 3)",
		`nul`:                "Expecting value: line 1 column 1 (char 0)",
		`  `:                 "Expecting value: line 1 column 3 (char 2)",
		`{"a":}`:             "Expecting value: line 1 column 6 (char 5)",
		`1 2`:                "Extra data: line 1 column 3 (char 2)",
		"{\n\"ä\": x}":       "Expecting value: line 2 column 6 (char 7)",
		``:                   "Expecting value: line 1 column 1 (char 0)",
		`{not json`:          "Expecting property name enclosed in double quotes: line 1 column 2 (char 1)",
		`{"secret": "a\nb"}`: "",
	}
	for text, want := range cases {
		_, err := UnmarshalString(text)
		got := ""
		if err != nil {
			got = err.Error()
		}
		if got != want {
			t.Errorf("%q: %q, want %q", text, got, want)
		}
	}
}
