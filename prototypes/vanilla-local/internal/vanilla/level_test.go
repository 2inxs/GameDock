package vanilla

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestValidateWorld(t *testing.T) {
	root := t.TempDir()
	level := filepath.Join(root, "level.dat")
	makeNBT := func(version uint32) []byte {
		data := []byte("\x0a\x00\x00\x0a\x00\x04Data\x03\x00\x0bDataVersion")
		return append(binary.BigEndian.AppendUint32(data, version), 0, 0)
	}
	for _, tt := range []struct {
		name   string
		nbt    []byte
		badCRC bool
		ok     bool
	}{
		{name: "valid", nbt: makeNBT(4671), ok: true},
		{name: "different release", nbt: makeNBT(4672)},
		{name: "missing compound ends", nbt: makeNBT(4671)[:len(makeNBT(4671))-2]},
		{name: "bad gzip checksum", nbt: makeNBT(4671), badCRC: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var compressed bytes.Buffer
			gzipFile := gzip.NewWriter(&compressed)
			if _, err := gzipFile.Write(tt.nbt); err != nil {
				t.Fatal(err)
			}
			if err := gzipFile.Close(); err != nil {
				t.Fatal(err)
			}
			data := compressed.Bytes()
			if tt.badCRC {
				data[len(data)-8] ^= 1
			}
			if err := os.WriteFile(level, data, 0600); err != nil {
				t.Fatal(err)
			}
			if err := ValidateWorld(root); (err == nil) != tt.ok {
				t.Fatalf("ValidateWorld: %v, wanted success = %v", err, tt.ok)
			}
		})
	}
}
