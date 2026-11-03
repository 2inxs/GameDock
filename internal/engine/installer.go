// Package engine installs a pinned, portable EasyTier runtime. It never installs
// drivers, creates services, starts the runtime, or changes the system network.
package engine

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	Version               = "2.6.4"
	DownloadURL           = "https://github.com/EasyTier/EasyTier/releases/download/v2.6.4/easytier-windows-x86_64-v2.6.4.zip"
	ArchiveSHA256         = "27af91e270e554709b048bd32327fefd2dfce5062ae1e8701af7550c6f525f84"
	coreSHA256            = "da7eb2d24b5416f3d3407636949e964a0750e3f9dc53a828cb6799a57ead445d"
	cliSHA256             = "d8783e851e944b44a9b71b39fd02f227ec0a2a82b3165c55ead5dd32dcde53a1"
	maxArchiveBytes int64 = 64 * 1024 * 1024
	maxBinaryBytes  int64 = 128 * 1024 * 1024
	manifestName          = "install-manifest.json"
)

type Info struct {
	Installed  bool   `json:"installed"`
	Version    string `json:"version"`
	Path       string `json:"path"`
	Error      string `json:"error"`
	Installing bool   `json:"installing"`
}

type Installer struct {
	mu          sync.Mutex
	root        string
	rootError   string
	installing  bool
	lastError   string
	client      *http.Client
	downloadURL string
	archiveSHA  string
	binarySHA   map[string]string
	supported   bool
}

type installManifest struct {
	Version       string            `json:"version"`
	Source        string            `json:"source"`
	ArchiveSHA256 string            `json:"archiveSha256"`
	Binaries      map[string]string `json:"binaries"`
}

func New(root string) *Installer {
	i := &Installer{root: filepath.Clean(root), downloadURL: DownloadURL, archiveSHA: ArchiveSHA256,
		binarySHA: map[string]string{
			"easytier-core.exe": coreSHA256,
			"easytier-cli.exe": cliSHA256,
			"Packet.dll": "c7c03a87eac7243ccbe331554624b18803010b740e311fc8cfddb573096eacac",
		},
		supported: runtime.GOOS == "windows" && runtime.GOARCH == "amd64",
		client: &http.Client{Timeout: 3 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 || req.URL.Scheme != "https" {
				return errors.New("небезопасное перенаправление загрузки")
			}
			return nil
		}},
	}
	if root == "" || !filepath.IsAbs(root) {
		i.rootError = "укажите абсолютный путь к папке движка EasyTier"
	}
	return i
}

// Paths are fixed even before installation. Call Info and require Installed
// before executing either file; a filename alone is not an integrity check.
func (i *Installer) CorePath() string { return filepath.Join(i.root, Version, "easytier-core.exe") }
func (i *Installer) CLIPath() string  { return filepath.Join(i.root, Version, "easytier-cli.exe") }

// Info performs local integrity checks only and never downloads anything.
func (i *Installer) Info() Info {
	i.mu.Lock()
	installing, previousError := i.installing, i.lastError
	i.mu.Unlock()
	info := Info{Version: Version, Installing: installing}
	if i.rootError != "" {
		info.Error = i.rootError
		return info
	}
	if !i.supported {
		info.Error = "автоматическая установка EasyTier поддерживает только Windows x64"
		return info
	}
	dir := filepath.Join(i.root, Version)
	stat, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		info.Error = previousError
		return info
	}
	if err != nil || !stat.IsDir() || stat.Mode()&os.ModeSymlink != 0 {
		info.Error = "папка EasyTier недоступна или имеет неверный тип"
		return info
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != len(i.binarySHA)+1 {
		info.Error = "папка EasyTier неполна или содержит посторонние файлы"
		return info
	}
	for _, entry := range entries {
		if _, expected := i.binarySHA[entry.Name()]; entry.Name() != manifestName && !expected {
			info.Error = "папка EasyTier содержит посторонние файлы; запуск запрещён"
			return info
		}
	}
	manifestPath := filepath.Join(dir, manifestName)
	manifestInfo, err := os.Lstat(manifestPath)
	if err != nil || !manifestInfo.Mode().IsRegular() || manifestInfo.Size() > 16*1024 {
		info.Error = "манифест EasyTier отсутствует или имеет неверный тип/размер"
		return info
	}
	f, err := os.Open(manifestPath)
	if err != nil {
		info.Error = "установка EasyTier неполна: отсутствует проверенный манифест"
		return info
	}
	actual, readErr := io.ReadAll(io.LimitReader(f, 16*1024+1))
	closeErr := f.Close()
	expected, _ := i.manifestBytes()
	if readErr != nil || closeErr != nil || !bytes.Equal(actual, expected) {
		info.Error = "манифест EasyTier повреждён или не соответствует закреплённой версии"
		return info
	}
	for name := range i.binarySHA {
		file := filepath.Join(dir, name)
		stat, err := os.Lstat(file)
		if err != nil || !stat.Mode().IsRegular() || stat.Size() <= 0 || stat.Size() > maxBinaryBytes {
			info.Error = "файлы EasyTier отсутствуют или имеют неверный тип/размер"
			return info
		}
		f, err := os.Open(file)
		if err != nil {
			info.Error = "не удалось проверить файлы EasyTier"
			return info
		}
		hash := sha256.New()
		_, copyErr := io.Copy(hash, io.LimitReader(f, maxBinaryBytes+1))
		closeErr := f.Close()
		if copyErr != nil || closeErr != nil || hex.EncodeToString(hash.Sum(nil)) != i.binarySHA[name] {
			info.Error = "контрольная сумма файлов EasyTier не совпадает; запуск запрещён"
			return info
		}
	}
	info.Installed, info.Path = true, i.CorePath()
	return info
}

func (i *Installer) Install(ctx context.Context) (info Info, installErr error) {
	i.mu.Lock()
	if i.installing {
		i.mu.Unlock()
		return i.Info(), errors.New("EasyTier уже устанавливается")
	}
	i.installing = true
	i.mu.Unlock()
	defer func() {
		i.mu.Lock()
		i.installing = false
		i.lastError = ""
		if installErr != nil {
			i.lastError = installErr.Error()
		}
		i.mu.Unlock()
		info = i.Info()
		if installErr != nil {
			info.Error = installErr.Error()
		}
	}()
	if existing := i.Info(); existing.Installed {
		return existing, nil
	}
	if i.rootError != "" {
		return Info{}, errors.New(i.rootError)
	}
	if !i.supported {
		return Info{}, errors.New("автоматическая установка EasyTier поддерживает только Windows x64")
	}
	target := filepath.Join(i.root, Version)
	if _, err := os.Lstat(target); err == nil {
		return Info{}, errors.New("существующая папка EasyTier не прошла проверку; закройте подключения и переместите повреждённую папку " + target + " перед повторной установкой")
	} else if !errors.Is(err, os.ErrNotExist) {
		return Info{}, fmt.Errorf("не удалось проверить папку EasyTier: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return Info{}, fmt.Errorf("установка EasyTier отменена: %w", err)
	}
	if err := os.MkdirAll(i.root, 0700); err != nil {
		return Info{}, fmt.Errorf("не удалось создать папку EasyTier: %w", err)
	}
	archive, err := i.download(ctx)
	if err != nil {
		return Info{}, err
	}
	defer os.Remove(archive)
	stage, err := os.MkdirTemp(i.root, ".install-"+Version+"-*")
	if err != nil {
		return Info{}, fmt.Errorf("не удалось подготовить установку EasyTier: %w", err)
	}
	if !ownedChild(i.root, stage) {
		return Info{}, errors.New("небезопасный путь временной установки EasyTier")
	}
	defer os.RemoveAll(stage) // stage was created and checked inside the exact configured root.
	if err := i.extract(ctx, archive, stage); err != nil {
		return Info{}, err
	}
	manifest, err := i.manifestBytes()
	if err != nil {
		return Info{}, errors.New("не удалось создать манифест EasyTier")
	}
	if err = writeSynced(filepath.Join(stage, manifestName), bytes.NewReader(manifest), 0600); err != nil {
		return Info{}, fmt.Errorf("не удалось сохранить манифест EasyTier: %w", err)
	}
	if err = ctx.Err(); err != nil {
		return Info{}, fmt.Errorf("установка EasyTier отменена: %w", err)
	}
	// A valid existing directory is never replaced, including a concurrent install.
	if _, err = os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		return Info{}, errors.New("папка EasyTier появилась во время установки; существующие файлы сохранены")
	}
	if err = os.Rename(stage, target); err != nil {
		return Info{}, fmt.Errorf("не удалось завершить установку EasyTier: %w", err)
	}
	return Info{}, nil
}

func (i *Installer) manifestBytes() ([]byte, error) {
	return json.MarshalIndent(installManifest{Version: Version, Source: i.downloadURL, ArchiveSHA256: i.archiveSHA, Binaries: i.binarySHA}, "", "  ")
}

func (i *Installer) download(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, i.downloadURL, nil)
	if err != nil {
		return "", errors.New("неверный адрес загрузки EasyTier")
	}
	req.Header.Set("User-Agent", "GameDock-EasyTier-Installer/"+Version)
	response, err := i.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("не удалось загрузить EasyTier: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("сервер загрузки EasyTier вернул HTTP %d", response.StatusCode)
	}
	if response.ContentLength > maxArchiveBytes {
		return "", errors.New("архив EasyTier превышает допустимый размер 64 МиБ")
	}
	f, err := os.CreateTemp(i.root, ".download-*.zip")
	if err != nil {
		return "", fmt.Errorf("не удалось создать временный архив EasyTier: %w", err)
	}
	name := f.Name()
	success := false
	defer func() {
		if !success {
			os.Remove(name)
		}
	}()
	hash := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, hash), io.LimitReader(response.Body, maxArchiveBytes+1))
	if copyErr == nil {
		copyErr = f.Sync()
	}
	closeErr := f.Close()
	if copyErr != nil || closeErr != nil {
		return "", errors.New("не удалось полностью загрузить и сохранить архив EasyTier")
	}
	if n > maxArchiveBytes {
		return "", errors.New("архив EasyTier превышает допустимый размер 64 МиБ")
	}
	if hex.EncodeToString(hash.Sum(nil)) != i.archiveSHA {
		return "", errors.New("контрольная сумма архива EasyTier не совпадает; установка отменена")
	}
	success = true
	return name, nil
}

func (i *Installer) extract(ctx context.Context, archive, stage string) error {
	z, err := zip.OpenReader(archive)
	if err != nil {
		return errors.New("архив EasyTier повреждён")
	}
	defer z.Close()
	if len(z.File) > 128 {
		return errors.New("архив EasyTier содержит слишком много записей")
	}
	selected := make(map[string]*zip.File)
	for _, entry := range z.File {
		if !safeArchiveName(entry.Name) || entry.Mode()&os.ModeSymlink != 0 || (!entry.FileInfo().IsDir() && !entry.Mode().IsRegular()) {
			return errors.New("архив EasyTier содержит небезопасный путь или тип файла")
		}
		name := path.Base(entry.Name)
		if _, wanted := i.binarySHA[name]; !wanted {
			continue
		}
		if entry.FileInfo().IsDir() || selected[name] != nil || entry.UncompressedSize64 == 0 || entry.UncompressedSize64 > uint64(maxBinaryBytes) {
			return errors.New("архив EasyTier содержит повторяющийся файл или недопустимый размер")
		}
		selected[name] = entry
	}
	if len(selected) != len(i.binarySHA) {
		return errors.New("в архиве EasyTier отсутствует обязательный файл")
	}
	for name := range i.binarySHA {
		entry, err := selected[name].Open()
		if err != nil {
			return errors.New("не удалось прочитать исполняемый файл EasyTier")
		}
		hash := sha256.New()
		reader := io.TeeReader(io.LimitReader(contextReader{ctx: ctx, reader: entry}, maxBinaryBytes+1), hash)
		err = writeSynced(filepath.Join(stage, name), reader, 0700)
		closeErr := entry.Close()
		if err != nil || closeErr != nil || hex.EncodeToString(hash.Sum(nil)) != i.binarySHA[name] {
			return errors.New("исполняемый файл EasyTier не прошёл проверку; установка отменена")
		}
	}
	return nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func writeSynced(name string, reader io.Reader, mode os.FileMode) error {
	f, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	n, copyErr := io.Copy(f, reader)
	if copyErr == nil && n > maxBinaryBytes {
		copyErr = errors.New("файл превышает допустимый размер")
	}
	if copyErr == nil {
		copyErr = f.Sync()
	}
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func safeArchiveName(name string) bool {
	if name == "" || len(name) > 512 || strings.ContainsAny(name, "\\:\x00") || strings.HasPrefix(name, "/") {
		return false
	}
	trimmed := strings.TrimSuffix(name, "/")
	return trimmed != "" && path.Clean(trimmed) == trimmed && trimmed != "." && trimmed != ".." && !strings.HasPrefix(trimmed, "../")
}

func ownedChild(root, child string) bool {
	if !filepath.IsAbs(root) || !filepath.IsAbs(child) {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(child))
	return err == nil && rel != "." && rel != ".." && !filepath.IsAbs(rel) && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
