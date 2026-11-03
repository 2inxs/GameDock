package engine

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

var fakeBinaries = map[string][]byte{"easytier-core.exe": []byte("fake portable core fixture"), "easytier-cli.exe": []byte("fake portable CLI fixture")}

func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

func testArchive(t *testing.T, extras map[string][]byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	z := zip.NewWriter(&buffer)
	for name, data := range fakeBinaries {
		f, err := z.Create("easytier-windows-x86_64/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	for name, data := range extras {
		f, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func fakeInstaller(t *testing.T, archive []byte) (*Installer, *atomic.Int32) {
	t.Helper()
	count := &atomic.Int32{}
	i := New(t.TempDir())
	i.client, i.downloadURL, i.archiveSHA, i.supported = fixtureClient(archive, count), "https://fixture.invalid/release.zip", digest(archive), true
	i.binarySHA = make(map[string]string)
	for name, data := range fakeBinaries {
		i.binarySHA[name] = digest(data)
	}
	return i, count
}

type fixtureTransport func(*http.Request) (*http.Response, error)

func (f fixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func fixtureClient(data []byte, count *atomic.Int32) *http.Client {
	return &http.Client{Transport: fixtureTransport(func(r *http.Request) (*http.Response, error) {
		if count != nil {
			count.Add(1)
		}
		return &http.Response{StatusCode: http.StatusOK, ContentLength: int64(len(data)), Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(data)), Request: r}, nil
	})}
}

func TestInstallOnlyPinnedPortableExecutablesAndIdempotence(t *testing.T) {
	i, requests := fakeInstaller(t, testArchive(t, map[string][]byte{"easytier-windows-x86_64/wintun.dll": []byte("must not be installed"), "easytier-windows-x86_64/WinDivert64.sys": []byte("must not be installed")}))
	if info := i.Info(); info.Installed || info.Installing || info.Error != "" || requests.Load() != 0 {
		t.Fatal("missing Info must be local and harmless")
	}
	info, err := i.Install(context.Background())
	if err != nil || !info.Installed || info.Installing || info.Path != i.CorePath() {
		t.Fatalf("install = %+v, %v", info, err)
	}
	entries, err := os.ReadDir(filepath.Join(i.root, Version))
	if err != nil || len(entries) != 3 {
		t.Fatal("installer extracted files beyond two executables and manifest")
	}
	before, _ := os.ReadFile(i.CorePath())
	if _, err = i.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(i.CorePath())
	if requests.Load() != 1 || !bytes.Equal(before, after) {
		t.Fatal("valid installation was downloaded or overwritten again")
	}
	rootFiles, _ := os.ReadDir(i.root)
	if len(rootFiles) != 1 {
		t.Fatal("successful install left download or staging files")
	}
}

func TestTamperedFilesAndManifestAreRejectedAndPreserved(t *testing.T) {
	i, requests := fakeInstaller(t, testArchive(t, nil))
	if _, err := i.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(i.CorePath(), []byte("tampered executable"), 0700); err != nil {
		t.Fatal(err)
	}
	// A forged local manifest cannot change compiled binary pins.
	manifest := installManifest{Version: Version, Source: i.downloadURL, ArchiveSHA256: i.archiveSHA, Binaries: map[string]string{"easytier-core.exe": digest([]byte("tampered executable")), "easytier-cli.exe": i.binarySHA["easytier-cli.exe"]}}
	forged, _ := json.MarshalIndent(manifest, "", "  ")
	if err := os.WriteFile(filepath.Join(i.root, Version, manifestName), forged, 0600); err != nil {
		t.Fatal(err)
	}
	if info := i.Info(); info.Installed || info.Error == "" {
		t.Fatal("tampered installation passed local integrity validation")
	}
	if _, err := i.Install(context.Background()); err == nil {
		t.Fatal("replaced an existing invalid installation")
	}
	if requests.Load() != 1 {
		t.Fatal("downloaded while an existing corrupt directory blocks replacement")
	}
	actual, _ := os.ReadFile(i.CorePath())
	if string(actual) != "tampered executable" {
		t.Fatal("installer overwrote possibly in-use files")
	}
}

func TestUnexpectedSideLoadedFilesInvalidateInstallation(t *testing.T) {
	i, _ := fakeInstaller(t, testArchive(t, nil))
	if _, err := i.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(i.root, Version, "wintun.dll"), []byte("unexpected side-loaded library"), 0600); err != nil {
		t.Fatal(err)
	}
	if info := i.Info(); info.Installed || info.Error == "" {
		t.Fatal("accepted unexpected runtime-side files")
	}
}

func TestRejectsArchiveChecksumUnsafePathsAndDuplicateBinary(t *testing.T) {
	cases := map[string]map[string][]byte{
		"traversal": {"../escape.exe": []byte("bad")}, "absolute": {"/escape.exe": []byte("bad")},
		"windows traversal": {`..\escape.exe`: []byte("bad")}, "drive path": {"C:/escape.exe": []byte("bad")},
		"duplicate binary": {"another/easytier-core.exe": fakeBinaries["easytier-core.exe"]},
	}
	for name, extras := range cases {
		t.Run(name, func(t *testing.T) {
			i, _ := fakeInstaller(t, testArchive(t, extras))
			if _, err := i.Install(context.Background()); err == nil {
				t.Fatal("accepted an unsafe archive")
			}
			assertNoInstalledOrTemporaryFiles(t, i)
		})
	}
	t.Run("wrong archive checksum", func(t *testing.T) {
		i, _ := fakeInstaller(t, testArchive(t, nil))
		i.archiveSHA = strings.Repeat("0", 64)
		if _, err := i.Install(context.Background()); err == nil {
			t.Fatal("accepted an unpinned archive")
		}
		assertNoInstalledOrTemporaryFiles(t, i)
	})
	t.Run("wrong binary checksum", func(t *testing.T) {
		i, _ := fakeInstaller(t, testArchive(t, nil))
		i.binarySHA["easytier-cli.exe"] = strings.Repeat("0", 64)
		if _, err := i.Install(context.Background()); err == nil {
			t.Fatal("accepted an unpinned executable")
		}
		assertNoInstalledOrTemporaryFiles(t, i)
	})
}

func TestRejectsOversizeBinaryHeaderAndDownload(t *testing.T) {
	archive := append([]byte(nil), testArchive(t, nil)...)
	central := bytes.Index(archive, []byte{'P', 'K', 1, 2})
	if central < 0 {
		t.Fatal("fixture lacks central directory")
	}
	binary.LittleEndian.PutUint32(archive[central+24:central+28], uint32(maxBinaryBytes+1))
	i, _ := fakeInstaller(t, archive)
	if _, err := i.Install(context.Background()); err == nil {
		t.Fatal("accepted a binary exceeding the extraction limit")
	}
	assertNoInstalledOrTemporaryFiles(t, i)
	i, _ = fakeInstaller(t, testArchive(t, nil))
	i.client = &http.Client{Transport: fixtureTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, ContentLength: maxArchiveBytes + 1, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})}
	if _, err := i.Install(context.Background()); err == nil {
		t.Fatal("accepted an oversize download")
	}
	assertNoInstalledOrTemporaryFiles(t, i)
}

func TestCanceledInstallAndPartialInstallation(t *testing.T) {
	i, count := fakeInstaller(t, testArchive(t, nil))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if info, err := i.Install(ctx); err == nil || info.Installing {
		t.Fatal("canceled install did not reset installing state")
	}
	if count.Load() != 0 {
		t.Fatal("canceled install started a download")
	}
	if err := os.MkdirAll(filepath.Join(i.root, Version), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(i.CorePath(), fakeBinaries["easytier-core.exe"], 0700); err != nil {
		t.Fatal(err)
	}
	if info := i.Info(); info.Installed || info.Error == "" {
		t.Fatal("accepted a partial installation without a manifest/CLI")
	}
}

func TestPinnedOfficialArchiveSmoke(t *testing.T) {
	// Optional fixture smoke test uses the already downloaded official release.
	// Actual portable binaries are staged exclusively inside this workspace.
	work, err := filepath.Abs(filepath.Join("..", "..", "..", "..", "work"))
	if err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(work, "easytier-dist", "easytier-windows-x86_64-v2.6.4.zip")
	data, err := os.ReadFile(archivePath)
	if os.IsNotExist(err) {
		t.Skip("official archive fixture not present")
	}
	if err != nil {
		t.Fatal(err)
	}
	if digest(data) != ArchiveSHA256 {
		t.Fatal("official archive fixture does not match pinned SHA256")
	}
	root, err := os.MkdirTemp(work, "engine-smoke-*")
	if err != nil {
		t.Fatal(err)
	}
	if !ownedChild(work, root) {
		t.Fatal("unsafe smoke test root")
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	i := New(root)
	i.client, i.supported = fixtureClient(data, nil), true
	info, err := i.Install(context.Background())
	if err != nil || !info.Installed {
		t.Fatalf("official archive extraction/integrity = %+v, %v", info, err)
	}
	// The compiled binary pins were derived from this archive, not a mutable manifest.
	if actual := i.Info(); !actual.Installed || actual.Error != "" {
		t.Fatalf("official binary pins did not validate: %+v", actual)
	}
	if runtime.GOOS == "windows" && runtime.GOARCH == "amd64" {
		cmd := exec.Command(i.CorePath(), "--version")
		cmd.Dir = root
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("installed EasyTier cannot start: %v: %s", err, output)
		}
	}
}

func assertNoInstalledOrTemporaryFiles(t *testing.T, i *Installer) {
	t.Helper()
	if info := i.Info(); info.Installed || info.Installing {
		t.Fatal("failed install is reported as active or installed")
	}
	entries, err := os.ReadDir(i.root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatal("failed install left staged files or a partial version directory")
	}
}
