package syncer

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

type fakeDrive struct {
	uploadErr  error
	failName   string
	uploaded   []string
	folders    []string
	folderNext int
	rootID     string
	rootName   string
	rootErr    error
}

func (f *fakeDrive) ResolveRootFolder(_ context.Context, name string) (string, error) {
	f.rootName = name
	return f.rootID, f.rootErr
}

func (f *fakeDrive) EnsureFolder(_ context.Context, name, parentID string) (string, error) {
	f.folderNext++
	f.folders = append(f.folders, filepath.Join(parentID, name))
	return name, nil
}

func (f *fakeDrive) UploadFile(_ context.Context, name, parentID string, contents io.Reader) error {
	_, _ = io.Copy(io.Discard, contents)
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
