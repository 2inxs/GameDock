package world

import (
	"bufio"
	"errors"
	"io/fs"
	"os"
	"strings"
)

const propertyWhitespace = " \t\f"

// Accept standard server.properties syntax. Reject escaped property names and
// continued lines so a different Java parser result cannot redirect the world.
func checkLevelName(path string) error {
	file, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	lines := bufio.NewScanner(file)
	lines.Split(splitPropertyLine)
	for lines.Scan() {
		line := strings.TrimLeft(strings.TrimPrefix(lines.Text(), "\ufeff"), propertyWhitespace)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "!") {
			continue
		}
		trailingSlashes := len(line) - len(strings.TrimRight(line, `\`))
		if trailingSlashes%2 != 0 {
			return errors.New("переносы строк через обратную косую черту в server.properties не поддерживаются")
		}
		end := strings.IndexAny(line, propertyWhitespace+"=:")
		key, value := line, ""
		if end != -1 {
			key = line[:end]
			value = strings.TrimLeft(line[end:], propertyWhitespace)
			if strings.HasPrefix(value, "=") || strings.HasPrefix(value, ":") {
				value = strings.TrimLeft(value[1:], propertyWhitespace)
			}
		}
		if strings.Contains(key, `\`) {
			return errors.New("обратная косая черта в имени параметра server.properties не поддерживается")
		}
		if key == "level-name" && value != "world" {
			return errors.New("в server.properties параметр level-name должен иметь значение world")
		}
		if key == "online-mode" && value != "true" {
			return errors.New("поддерживается импорт только с online-mode=true, чтобы сохранить соответствие аккаунтов данным игроков")
		}
	}
	return lines.Err()
}

// Java properties accept CR, LF and CRLF as line endings.
func splitPropertyLine(data []byte, atEOF bool) (int, []byte, error) {
	for index, char := range data {
		if char == '\r' || char == '\n' {
			return index + 1, data[:index], nil
		}
	}
	if atEOF && len(data) != 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}
