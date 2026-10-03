package control

import (
	"bytes"
	"strings"
	"testing"
)

func TestCopyPublicDumpSkipsOnlySchemaCreation(t *testing.T) {
	input := "header\nCREATE SCHEMA public;\nCREATE TABLE public.items (id integer);\n" + strings.Repeat("x", 128*1024) + "\n"
	var output bytes.Buffer
	if err := copyPublicDump(&output, strings.NewReader(input)); err != nil {
		t.Fatal(err)
	}
	want := "header\nCREATE TABLE public.items (id integer);\n" + strings.Repeat("x", 128*1024) + "\n"
	if output.String() != want {
		t.Fatal("public dump filtering changed unexpected content")
	}
}
