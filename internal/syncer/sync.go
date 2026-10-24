package syncer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"google.golang.org/api/drive/v3"
)

type driveAPI interface {
	ResolveRootFolder(ctx context.Context, name string) (string, error)
	EnsureFolder(ctx context.Context, name, parentID string) (string, error)
	UploadFile(ctx context.Context, name, parentID string, contents io.Reader) error
}

type googleDriveAPI struct {
	service *drive.Service
}

func (g googleDriveAPI) EnsureFolder(ctx context.Context, name, parentID string) (string, error) {
	query := fmt.Sprintf("name = '%s' and mimeType = '%s' and '%s' in parents and trashed = false",
		escapeDriveQueryValue(name), "application/vnd.google-apps.folder", escapeDriveQueryValue(parentID))
	found, err := g.service.Files.List().Context(ctx).Q(query).PageSize(1).
		Fields("files(id)").SupportsAllDrives(true).IncludeItemsFromAllDrives(true).Do()
	if err != nil {
		return "", err
	}
	if len(found.Files) > 0 {
		return found.Files[0].Id, nil
	}

	folder, err := g.service.Files.Create(&drive.File{
		Name:     name,
		MimeType: "application/vnd.google-apps.folder",
		Parents:  []string{parentID},
	}).Context(ctx).SupportsAllDrives(true).Do()
	if err != nil {
		return "", err
	}
	return folder.Id, nil
}

func (g googleDriveAPI) ResolveRootFolder(ctx context.Context, name string) (string, error) {
	query := fmt.Sprintf("name = '%s' and mimeType = '%s' and 'root' in parents and trashed = false",
		escapeDriveQueryValue(name), "application/vnd.google-apps.folder")
	pageToken := ""
	var matches []*drive.File
	for {
		result, err := g.service.Files.List().Context(ctx).Q(query).PageSize(100).
			Fields("nextPageToken,files(id,name)").PageToken(pageToken).
			SupportsAllDrives(true).IncludeItemsFromAllDrives(true).Do()
		if err != nil {
			return "", err
		}
		matches = append(matches, result.Files...)
		if len(matches) > 1 {
			return "", fmt.Errorf("multiple folders named %q found in the Drive root; use the folder ID instead", name)
		}
		if result.NextPageToken == "" {
			break
		}
		pageToken = result.NextPageToken
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("no folder named %q found in the Drive root", name)
	}
	return matches[0].Id, nil
}

func (g googleDriveAPI) UploadFile(ctx context.Context, name, parentID string, contents io.Reader) error {
	_, err := g.service.Files.Create(&drive.File{
		Name:    name,
		Parents: []string{parentID},
	}).Context(ctx).Media(contents).SupportsAllDrives(true).Do()
	return err
}

// Sync uploads regular files under localDir, retaining their relative folder
// structure. A local file is removed only after Drive confirms the upload and
// the file has not changed while it was being uploaded.
func Sync(ctx context.Context, service *drive.Service, localDir, driveFolderName string) error {
	return syncByFolderName(ctx, googleDriveAPI{service: service}, localDir, driveFolderName)
}

func SyncByID(ctx context.Context, service *drive.Service, localDir, driveFolderID string) error {
	return syncWithAPI(ctx, googleDriveAPI{service: service}, localDir, driveFolderID)
}

func syncByFolderName(ctx context.Context, api driveAPI, localDir, driveFolderName string) error {
	folderID, err := api.ResolveRootFolder(ctx, driveFolderName)
	if err != nil {
		return fmt.Errorf("resolve Drive folder %q: %w", driveFolderName, err)
	}
	return syncWithAPI(ctx, api, localDir, folderID)
}

func syncWithAPI(ctx context.Context, api driveAPI, localDir, driveFolderID string) error {
	rootInfo, err := os.Stat(localDir)
	if err != nil {
		return fmt.Errorf("stat local directory: %w", err)
	}
	if !rootInfo.IsDir() {
		return fmt.Errorf("local_dir is not a directory: %s", localDir)
	}

	absRoot, err := filepath.Abs(localDir)
	if err != nil {
		return fmt.Errorf("resolve local directory: %w", err)
	}

	folderIDs := map[string]string{".": driveFolderID}
	var syncErrors []error
	walkErr := filepath.WalkDir(absRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("walk %s: %w", path, walkErr)
		}
		if path == absRoot {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}

		relative, err := filepath.Rel(absRoot, path)
		if err != nil {
			return fmt.Errorf("resolve relative path for %s: %w", path, err)
		}
		parentPath := filepath.Dir(relative)
		parentID, ok := folderIDs[parentPath]
		if !ok {
			parentID, err = ensureDrivePath(ctx, api, folderIDs, parentPath, driveFolderID)
			if err != nil {
				syncErrors = append(syncErrors, err)
				return nil
			}
		}

		if entry.IsDir() {
			if _, err := ensureDrivePath(ctx, api, folderIDs, relative, driveFolderID); err != nil {
				syncErrors = append(syncErrors, err)
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			syncErrors = append(syncErrors, fmt.Errorf("stat %s: %w", path, err))
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}

		file, err := os.Open(path)
		if err != nil {
			syncErrors = append(syncErrors, fmt.Errorf("open %s: %w", path, err))
			return nil
		}
		before, err := file.Stat()
		if err != nil {
			closeErr := file.Close()
			syncErrors = append(syncErrors, errors.Join(fmt.Errorf("stat opened file %s: %w", path, err), closeErr))
			return nil
		}
		if err := api.UploadFile(ctx, entry.Name(), parentID, file); err != nil {
			closeErr := file.Close()
			syncErrors = append(syncErrors, errors.Join(fmt.Errorf("upload %s: %w", path, err), closeErr))
			return nil
		}
		if err := file.Close(); err != nil {
			syncErrors = append(syncErrors, fmt.Errorf("close uploaded file %s: %w", path, err))
			return nil
		}

		after, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			syncErrors = append(syncErrors, fmt.Errorf("stat after upload %s: %w", path, err))
			return nil
		}
		if !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
			syncErrors = append(syncErrors, fmt.Errorf("uploaded %s but it changed locally; leaving it in place", path))
			return nil
		}
		if err := os.Remove(path); err != nil {
			syncErrors = append(syncErrors, fmt.Errorf("uploaded %s but could not remove local file: %w", path, err))
			return nil
		}
		fmt.Printf("Uploaded and removed %s\n", path)
		return nil
	})
	return errors.Join(walkErr, errors.Join(syncErrors...))
}

func ensureDrivePath(ctx context.Context, api driveAPI, cache map[string]string, relative, rootID string) (string, error) {
	if id, ok := cache[relative]; ok {
		return id, nil
	}
	parent := filepath.Dir(relative)
	parentID := rootID
	if parent != "." {
		var err error
		parentID, err = ensureDrivePath(ctx, api, cache, parent, rootID)
		if err != nil {
			return "", err
		}
	}
	id, err := api.EnsureFolder(ctx, filepath.Base(relative), parentID)
	if err != nil {
		return "", fmt.Errorf("create Drive folder %q: %w", relative, err)
	}
	cache[relative] = id
	return id, nil
}

func escapeDriveQueryValue(value string) string {
	return strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(value)
}
