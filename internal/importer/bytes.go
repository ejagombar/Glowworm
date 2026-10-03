package importer

import (
	"bytes"
	"fmt"
	"io"
	"strconv"
)

func bytesReader(data []byte) io.Reader { return bytes.NewReader(data) }

var _ = strconv.Itoa
var _ = fmt.Sprintf
