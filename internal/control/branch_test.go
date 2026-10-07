package control

import (
	"bytes"
	"strings"
	"testing"
)

func TestFilterPublicSchemaTOCOmitsOnlySchemaEntry(t *testing.T) {
	input := `;
; Archive created at 2026-10-03
;
5; 2615 2200 SCHEMA - public postgres
215; 1259 999 TABLE public notes project_admin
216; 0 999 TABLE DATA public notes project_admin
` + strings.Repeat("x", 128*1024) + "\n"
	var output bytes.Buffer
	if err := filterPublicSchemaTOC(&output, strings.NewReader(input)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "SCHEMA - public") || !strings.Contains(output.String(), "TABLE DATA public notes") || !strings.Contains(output.String(), strings.Repeat("x", 128*1024)) {
		t.Fatal("TOC filtering omitted or changed an unexpected entry")
	}
}

func TestFilterPublicSchemaTOCRequiresExactlyOneEntry(t *testing.T) {
	for _, input := range []string{"; no schema\n", "1; 1 1 SCHEMA - public postgres\n2; 1 1 SCHEMA - public postgres\n"} {
		if err := filterPublicSchemaTOC(&bytes.Buffer{}, strings.NewReader(input)); err == nil {
			t.Fatalf("accepted invalid TOC %q", input)
		}
	}
}
