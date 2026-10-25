package syncer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeDrive struct {
	mu         sync.Mutex
	uploadErr  error
	failName   string
	uploaded   []string
	folders    []string
	folderNext int
	rootID     string
	rootName   string
	rootErr    error
	active     int
	maxActive  int
	gate       chan struct{}
	fourActive chan struct{}
	signaled   bool
}

func (f *fakeDrive) ResolveRootFolder(_ context.Context, name string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rootName = name
	return f.rootID, f.rootErr
}

func (f *fakeDrive) EnsureFolder(_ context.Context, name, parentID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.folderNext++
	f.folders = append(f.folders, filepath.Join(parentID, name))
	return name, nil
}

func (f *fakeDrive) UploadFile(_ context.Context, name, parentID string, contents io.Reader) error {
	f.mu.Lock()
	f.active++
	if f.active > f.maxActive {
		f.maxActive = f.active
	}
	if f.active == uploadWorkerCount && f.fourActive != nil && !f.signaled {
		close(f.fourActive)
		f.signaled = true
	}
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.active--
		f.mu.Unlock()
	}()

	_, _ = io.Copy(io.Discard, contents)
	if f.gate != nil {
		<-f.gate
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.uploaded = append(f.uploaded, filepath.Join(parentID, name))
	if name == f.failName {
		return f.uploadErr
	}
	return nil
}

func TestSyncRemovesOnlySuccessfullyUploadedFiles(t *testing.T) {
	root := t.TempDir()
	success := filepath.Join(root, "a-success.txt")
	failed := filepath.Join(root, "z-failed.txt")
	if err := os.WriteFile(success, []byte("success"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(failed, []byte("failure"), 0600); err != nil {
		t.Fatal(err)
	}

	api := &fakeDrive{uploadErr: errors.New("upload rejected"), failName: "z-failed.txt"}
	err := syncWithAPI(context.Background(), api, root, "drive-root")
	if err == nil {
		t.Fatal("expected upload error")
	}

	if _, err := os.Stat(success); err == nil {
		t.Errorf("successfully uploaded file still exists")
	}
	if _, err := os.Stat(failed); err != nil {
		t.Errorf("failed upload should remain on disk: %v", err)
	}
}

func TestSyncPreservesNestedFoldersAndLeavesFailedFile(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	filePath := filepath.Join(nested, "item.txt")
	if err := os.WriteFile(filePath, []byte("payload"), 0600); err != nil {
		t.Fatal(err)
	}

	api := &fakeDrive{uploadErr: errors.New("upload rejected"), failName: "item.txt"}
	if err := syncWithAPI(context.Background(), api, root, "drive-root"); err == nil {
		t.Fatal("expected upload error")
	}
	if len(api.uploaded) != 1 || api.uploaded[0] != filepath.Join("nested", "item.txt") {
		t.Fatalf("unexpected uploaded paths: %v", api.uploaded)
	}
	if _, err := os.Stat(filePath); err != nil {
		t.Errorf("failed upload should remain on disk: %v", err)
	}
}

func TestSyncDeletesFileAfterSuccessfulUpload(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "item.txt")
	if err := os.WriteFile(filePath, []byte("payload"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := syncWithAPI(context.Background(), &fakeDrive{}, root, "drive-root"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filePath); !os.IsNotExist(err) {
		t.Errorf("uploaded file still exists: %v", err)
	}
}

func TestSyncCreatesEmptyDirectories(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "empty"), 0700); err != nil {
		t.Fatal(err)
	}

	api := &fakeDrive{}
	if err := syncWithAPI(context.Background(), api, root, "drive-root"); err != nil {
		t.Fatal(err)
	}
	if len(api.folders) != 1 || api.folders[0] != filepath.Join("drive-root", "empty") {
		t.Fatalf("unexpected Drive folders: %v", api.folders)
	}
}

func TestSyncResolvesNamedRootFolderBeforeUploading(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "item.txt"), []byte("payload"), 0600); err != nil {
		t.Fatal(err)
	}
	api := &fakeDrive{rootID: "resolved-folder-id"}

	if err := syncByFolderName(context.Background(), api, root, "BACKUPS"); err != nil {
		t.Fatal(err)
	}
	if api.rootName != "BACKUPS" {
		t.Errorf("resolved folder name = %q, want BACKUPS", api.rootName)
	}
	if len(api.uploaded) != 1 || api.uploaded[0] != filepath.Join("resolved-folder-id", "item.txt") {
		t.Errorf("uploaded paths = %v, want file under resolved ID", api.uploaded)
	}
}

func TestSyncDoesNotUploadWhenNamedFolderCannotBeResolved(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "item.txt")
	if err := os.WriteFile(path, []byte("payload"), 0600); err != nil {
		t.Fatal(err)
	}
	api := &fakeDrive{rootErr: errors.New("no folder found")}

	if err := syncByFolderName(context.Background(), api, root, "BACKUPS"); err == nil {
		t.Fatal("expected folder resolution error")
	}
	if len(api.uploaded) != 0 {
		t.Errorf("uploaded files despite resolution failure: %v", api.uploaded)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("local file should remain after resolution failure: %v", err)
	}
}

func TestSyncUploadsFourFilesConcurrently(t *testing.T) {
	root := t.TempDir()
	for i := range 8 {
		path := filepath.Join(root, fmt.Sprintf("file-%d.txt", i))
		if err := os.WriteFile(path, []byte("payload"), 0600); err != nil {
			t.Fatal(err)
		}
	}

	api := &fakeDrive{
		gate:       make(chan struct{}),
		fourActive: make(chan struct{}),
	}
	done := make(chan error, 1)
	go func() {
		done <- syncWithAPI(context.Background(), api, root, "drive-root")
	}()

	select {
	case <-api.fourActive:
		close(api.gate)
	case <-time.After(5 * time.Second):
		close(api.gate)
		<-done
		t.Fatal("sync did not run four uploads concurrently")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	api.mu.Lock()
	defer api.mu.Unlock()
	if api.maxActive != uploadWorkerCount {
		t.Errorf("maximum concurrent uploads = %d, want %d", api.maxActive, uploadWorkerCount)
	}
	if len(api.uploaded) != 8 {
		t.Errorf("uploaded %d files, want 8", len(api.uploaded))
	}
}

func TestPrintUploadProgressIncludesUploadMetrics(t *testing.T) {
	var output bytes.Buffer
	printUploadProgress(&output, "archive.zip", 4096, 1024, 512)

	want := "Upload progress [archive.zip]: 25.0% | 1.0 KiB/4.0 KiB uploaded | 3.0 KiB remaining | 512 B/s\n"
	if output.String() != want {
		t.Fatalf("progress output = %q, want %q", output.String(), want)
	}
}

func TestUploadProgressReporterReportsPeriodically(t *testing.T) {
	var output bytes.Buffer
	uploaded := &atomic.Int64{}
	uploaded.Store(1024)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		reportUploadProgress(stop, 10*time.Millisecond, &output, "archive.zip", 4096, time.Now(), uploaded)
	}()

	time.Sleep(35 * time.Millisecond)
	close(stop)
	<-done

	if got := output.String(); !strings.Contains(got, "archive.zip") || !strings.Contains(got, "25.0%") {
		t.Fatalf("periodic progress output missing expected values: %q", got)
	}
}

func TestQuietSyncSuppressesPerFileOutput(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "item.txt"), []byte("payload"), 0600); err != nil {
		t.Fatal(err)
	}

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	originalStdout := os.Stdout
	os.Stdout = writer
	syncErr := syncWithAPIOptions(context.Background(), &fakeDrive{}, root, "drive-root", SyncOptions{Quiet: true})
	os.Stdout = originalStdout
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if syncErr != nil {
		t.Fatal(syncErr)
	}
	if len(output) != 0 {
		t.Errorf("quiet sync wrote per-file output: %q", output)
	}
}
