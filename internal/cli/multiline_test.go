package cli

import (
	"testing"
)

func TestNormalizeCommas_ListValues(t *testing.T) {
	in := "[\n  \"a\"\n  15\n  true\n]"
	want := "[\n  \"a\",\n  15,\n  true,\n]"
	got := NormalizeCommasInMultiline(in)
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	// idempotent
	if NormalizeCommasInMultiline(got) != want {
		t.Fatalf("not idempotent")
	}
}

func TestNormalizeCommas_MapValues(t *testing.T) {
	in := "{\n  a = 1\n  b = 2\n}"
	want := "{\n  a = 1,\n  b = 2\n}"
	got := NormalizeCommasInMultiline(in)
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestNormalizeCommas_ObjectSchema(t *testing.T) {
	in := "object({\n  a = string\n  b = number\n})"
	want := "object({\n  a = string,\n  b = number\n})"
	got := NormalizeCommasInMultiline(in)
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestNormalizeCommas_TupleSchema(t *testing.T) {
	in := "tuple([\n  string\n  number\n])"
	want := "tuple([\n  string,\n  number,\n])"
	got := NormalizeCommasInMultiline(in)
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestNormalizeCommas_StringsAndComments(t *testing.T) {
	in := "[\n  \"line1\\nline2\"\n  2 // comment\n  # another\n]"
	want := "[\n  \"line1\\nline2\",\n  2, // comment\n  # another\n]"
	got := NormalizeCommasInMultiline(in)
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestNormalizeCommas_AssignmentOnNextLine(t *testing.T) {
	in := "{\n  a =\n    1\n}"
	// Do not insert a comma between a = and 1; only trailing before close
	want := "{\n  a =\n    1\n}"
	got := NormalizeCommasInMultiline(in)
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestNormalizeCommas_NestedStructures(t *testing.T) {
	in := "[\n  [\n    1\n    2\n  ]\n  3\n]"
	want := "[\n  [\n    1,\n    2,\n  ],\n  3,\n]"
	got := NormalizeCommasInMultiline(in)
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestNormalizeCommas_NoOpSingleLine(t *testing.T) {
	in := "[1, 2]"
	if NormalizeCommasInMultiline(in) != in {
		t.Fatalf("single-line should be unchanged")
	}
}

func TestNormalizeMultilineForHistory_CompactsBracketBoundaries(t *testing.T) {
	in := "func(\n  {\n    a = 1\n  }\n)\n[ \n 1,\n]\n"
	want := "func({a = 1}) [ 1,]"
	got := NormalizeMultilineForHistory(in)
	if got != want {
		t.Fatalf("got:\n%q\nwant:\n%q", got, want)
	}
}
