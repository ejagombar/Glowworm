package importer

import (
	"archive/zip"
	"bytes"
	"io"
	"strings"
	"testing"
)

type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

// A small upload that unpacks to far more than any statement must be turned
// away before it is unpacked, not read into memory.
func TestReadXLSXRefusesAZipBomb(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("xl/worksheets/sheet1.xml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.CopyN(w, zeros{}, maxXLSXUnpacked+1); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if buf.Len() > 1<<20 {
		t.Fatalf("the test file is %d bytes: not much of a bomb", buf.Len())
	}
	_, _, err = ReadXLSX(buf.Bytes(), "")
	if err == nil || !strings.Contains(err.Error(), "unzip size exceeds") {
		t.Fatalf("err = %v, want the unzip limit", err)
	}
}
