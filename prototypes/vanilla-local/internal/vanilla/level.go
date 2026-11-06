package vanilla

import (
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const dataVersion = 4671

// ValidateWorld checks the version in the compressed Java Edition level.dat.
func ValidateWorld(dir string) error {
	file, err := os.Open(filepath.Join(dir, "level.dat"))
	if err != nil {
		return err
	}
	defer file.Close()
	compressed, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("не читается level.dat: %w", err)
	}
	defer compressed.Close()
	reader := &io.LimitedReader{R: compressed, N: (8 << 20) + 1}
	version, err := readVersion(reader)
	if err != nil {
		return fmt.Errorf("не удалось проверить версию мира: %w", err)
	}
	if _, err := readByte(reader); err != io.EOF || reader.N == 0 {
		return errors.New("неверное завершение или контрольная сумма level.dat")
	}
	if version != dataVersion {
		return fmt.Errorf("мир имеет DataVersion %d, требуется Vanilla %s (DataVersion %d)", version, Version, dataVersion)
	}
	return nil
}

func readVersion(reader io.Reader) (int32, error) {
	kind, err := readByte(reader)
	if err != nil {
		return 0, err
	}
	if kind != 10 {
		return 0, errors.New("корень NBT должен быть compound")
	}
	if _, err := readString(reader); err != nil {
		return 0, err
	}
	return findVersion(reader, false)
}

func findVersion(reader io.Reader, inData bool) (int32, error) {
	var version int32
	found := false
	for {
		kind, err := readByte(reader)
		if err != nil {
			return 0, err
		}
		if kind == 0 {
			if !found {
				return 0, errors.New("Data.DataVersion отсутствует")
			}
			return version, nil
		}
		name, err := readString(reader)
		if err != nil {
			return 0, err
		}
		if inData && name == "DataVersion" {
			if kind != 3 || found {
				return 0, errors.New("неоднозначное поле DataVersion")
			}
			if err := binary.Read(reader, binary.BigEndian, &version); err != nil {
				return 0, err
			}
			found = true
			continue
		}
		if !inData && name == "Data" {
			if kind != 10 || found {
				return 0, errors.New("неоднозначное поле Data")
			}
			version, err = findVersion(reader, true)
			if err != nil {
				return 0, err
			}
			found = true
			continue
		}
		if err := skipTag(reader, kind, 0); err != nil {
			return 0, err
		}
	}
}

func skipTag(reader io.Reader, kind byte, depth int) error {
	if depth > 48 {
		return errors.New("слишком глубокая структура NBT")
	}
	switch kind {
	case 1, 2, 3, 4, 5, 6:
		size := [...]int64{0, 1, 2, 4, 8, 4, 8}
		return discard(reader, size[kind])
	case 7, 11, 12:
		count, err := readCount(reader)
		if err != nil {
			return err
		}
		width := map[byte]int64{7: 1, 11: 4, 12: 8}
		return discard(reader, int64(count)*width[kind])
	case 8:
		_, err := readString(reader)
		return err
	case 9:
		element, err := readByte(reader)
		if err != nil {
			return err
		}
		count, err := readCount(reader)
		if err != nil {
			return err
		}
		for i := int32(0); i < count; i++ {
			if err := skipTag(reader, element, depth+1); err != nil {
				return err
			}
		}
		return nil
	case 10:
		for {
			child, err := readByte(reader)
			if err != nil || child == 0 {
				return err
			}
			if _, err := readString(reader); err != nil {
				return err
			}
			if err := skipTag(reader, child, depth+1); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("неизвестный NBT tag %d", kind)
	}
}

func readByte(reader io.Reader) (byte, error) {
	var data [1]byte
	_, err := io.ReadFull(reader, data[:])
	return data[0], err
}

func readString(reader io.Reader) (string, error) {
	var size uint16
	if err := binary.Read(reader, binary.BigEndian, &size); err != nil {
		return "", err
	}
	data := make([]byte, size)
	_, err := io.ReadFull(reader, data)
	return string(data), err
}

func readCount(reader io.Reader) (int32, error) {
	var count int32
	if err := binary.Read(reader, binary.BigEndian, &count); err != nil {
		return 0, err
	}
	if count < 0 || count > 8<<20 {
		return 0, errors.New("недопустимый размер NBT")
	}
	return count, nil
}

func discard(reader io.Reader, size int64) error {
	_, err := io.CopyN(io.Discard, reader, size)
	return err
}
