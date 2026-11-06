package vanilla

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const Version = "1.21.11"
const serverURL = "https://piston-data.mojang.com/v1/objects/64bb6d763bed0a9f1d632ec347938594144943ed/server.jar"
const serverSHA256 = "f83b8e093865806f931c7e34aae41b177d4c076335263dd124c75d6d65dd1726"
const maxJar = 80 << 20

const properties = `server-ip=127.0.0.1
server-port=25565
level-name=world
online-mode=true
enable-rcon=false
enable-query=false
enable-status=true
management-server-enabled=false
`

func VerifyJar(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > maxJar {
		return errors.New("ожидался обычный файл server.jar допустимого размера")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return verifyCopy(io.Discard, file)
}

func EnsureJar(ctx context.Context, path, offlineJar string) error {
	if VerifyJar(path) == nil {
		return nil
	}
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return errors.New("путь server.jar занят нестандартным файлом")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}

	var source io.ReadCloser
	if offlineJar != "" {
		file, err := os.Open(offlineJar)
		if err != nil {
			return err
		}
		source = file
	} else {
		client := http.Client{
			Timeout: 2 * time.Minute,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, serverURL, nil)
		if err != nil {
			return err
		}
		response, err := client.Do(request)
		if err != nil {
			return fmt.Errorf("загрузка Mojang: %w", err)
		}
		if response.StatusCode != http.StatusOK {
			response.Body.Close()
			return fmt.Errorf("загрузка Mojang: HTTP %d", response.StatusCode)
		}
		source = response.Body
	}
	defer source.Close()
	return replace(path, func(file *os.File) error { return verifyCopy(file, source) })
}

func verifyCopy(target io.Writer, source io.Reader) error {
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(target, hash), io.LimitReader(source, maxJar+1))
	if err != nil {
		return err
	}
	if size > maxJar || hex.EncodeToString(hash.Sum(nil)) != serverSHA256 {
		return errors.New("контрольная сумма server.jar не совпадает с закреплённым релизом")
	}
	return nil
}

func WriteLocalProperties(dir string) error {
	return replace(filepath.Join(dir, "server.properties"), func(file *os.File) error {
		_, err := io.WriteString(file, properties)
		return err
	})
}

func AcceptEULA(dir string) error {
	return replace(filepath.Join(dir, "eula.txt"), func(file *os.File) error {
		_, err := io.WriteString(file, "eula=true\n")
		return err
	})
}

func EULAAccepted(dir string) bool {
	path := filepath.Join(dir, "eula.txt")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 512 {
		return false
	}
	data, err := os.ReadFile(path)
	return err == nil && string(data) == "eula=true\n"
}

func replace(path string, write func(*os.File) error) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".gamedock-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err = write(file); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
