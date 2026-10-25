package syncer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

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

// SyncOptions controls optional sync behavior.
type SyncOptions struct {
	Quiet bool
}

const uploadWorkerCount = 4
const UPLOAD_PROGRESS_INTERVAL = 30 * time.Second

var outputMu sync.Mutex

type uploadJob struct {
	path     string
	name     string
	parentID string
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
	return SyncWithOptions(ctx, service, localDir, driveFolderName, SyncOptions{})
}

func SyncWithOptions(ctx context.Context, service *drive.Service, localDir, driveFolderName string, options SyncOptions) error {
	return syncByFolderNameWithOptions(ctx, googleDriveAPI{service: service}, localDir, driveFolderName, options)
}

func SyncByID(ctx context.Context, service *drive.Service, localDir, driveFolderID string) error {
	return SyncByIDWithOptions(ctx, service, localDir, driveFolderID, SyncOptions{})
}

func SyncByIDWithOptions(ctx context.Context, service *drive.Service, localDir, driveFolderID string, options SyncOptions) error {
	return syncWithAPIOptions(ctx, googleDriveAPI{service: service}, localDir, driveFolderID, options)
}

func syncByFolderName(ctx context.Context, api driveAPI, localDir, driveFolderName string) error {
	return syncByFolderNameWithOptions(ctx, api, localDir, driveFolderName, SyncOptions{})
}

func syncByFolderNameWithOptions(ctx context.Context, api driveAPI, localDir, driveFolderName string, options SyncOptions) error {
	folderID, err := api.ResolveRootFolder(ctx, driveFolderName)
	if err != nil {
		return fmt.Errorf("resolve Drive folder %q: %w", driveFolderName, err)
	}
	return syncWithAPIOptions(ctx, api, localDir, folderID, options)
}

func syncWithAPI(ctx context.Context, api driveAPI, localDir, driveFolderID string) error {
	return syncWithAPIOptions(ctx, api, localDir, driveFolderID, SyncOptions{})
}

func syncWithAPIOptions(ctx context.Context, api driveAPI, localDir, driveFolderID string, options SyncOptions) error {
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
	var uploads []uploadJob
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

		uploads = append(uploads, uploadJob{path: path, name: entry.Name(), parentID: parentID})
		return nil
	})

	if len(uploads) > 0 {
		workerCount := min(uploadWorkerCount, len(uploads))
		jobs := make(chan uploadJob)
		results := make(chan error, len(uploads))
		var workers sync.WaitGroup
		workers.Add(workerCount)
		for range workerCount {
			go func() {
				defer workers.Done()
				for job := range jobs {
					if err := uploadAndRemoveFileWithOptions(ctx, api, job.path, job.name, job.parentID, options); err != nil {
						results <- err
					}
				}
			}()
		}
		for _, job := range uploads {
			jobs <- job
		}
		close(jobs)
		workers.Wait()
		close(results)
		for err := range results {
			syncErrors = append(syncErrors, err)
		}
	}
	return errors.Join(walkErr, errors.Join(syncErrors...))
}

func uploadAndRemoveFile(ctx context.Context, api driveAPI, path, name, parentID string) error {
	return uploadAndRemoveFileWithOptions(ctx, api, path, name, parentID, SyncOptions{})
}

func uploadAndRemoveFileWithOptions(ctx context.Context, api driveAPI, path, name, parentID string, options SyncOptions) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	before, err := file.Stat()
	if err != nil {
		closeErr := file.Close()
		return errors.Join(fmt.Errorf("stat opened file %s: %w", path, err), closeErr)
	}
	uploaded := &atomic.Int64{}
	var stopProgress chan struct{}
	var progressStopped chan struct{}
	if !options.Quiet {
		stopProgress = make(chan struct{})
		progressStopped = make(chan struct{})
		started := time.Now()
		go func() {
			defer close(progressStopped)
			reportUploadProgress(stopProgress, UPLOAD_PROGRESS_INTERVAL, os.Stdout, name, before.Size(), started, uploaded)
		}()
	}
	uploadErr := api.UploadFile(ctx, name, parentID, &countingReader{reader: file, count: uploaded})
	if !options.Quiet {
		close(stopProgress)
		<-progressStopped
	}
	if uploadErr != nil {
		closeErr := file.Close()
		return errors.Join(fmt.Errorf("upload %s: %w", path, uploadErr), closeErr)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close uploaded file %s: %w", path, err)
	}

	after, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat after upload %s: %w", path, err)
	}
	if !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return fmt.Errorf("uploaded %s but it changed locally; leaving it in place", path)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("uploaded %s but could not remove local file: %w", path, err)
	}
	if !options.Quiet {
		outputMu.Lock()
		fmt.Printf("Uploaded and removed %s\n", path)
		outputMu.Unlock()
	}
	return nil
}

type countingReader struct {
	reader io.Reader
	count  *atomic.Int64
}

func (r *countingReader) Read(buffer []byte) (int, error) {
	n, err := r.reader.Read(buffer)
	if n > 0 {
		r.count.Add(int64(n))
	}
	return n, err
}

func reportUploadProgress(stop <-chan struct{}, interval time.Duration, output io.Writer, name string, total int64, started time.Time, uploaded *atomic.Int64) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	lastReport := started
	var lastUploaded int64
	for {
		select {
		case <-stop:
			return
		case now := <-ticker.C:
			current := uploaded.Load()
			elapsed := now.Sub(lastReport).Seconds()
			speed := int64(0)
			if elapsed > 0 {
				speed = int64(float64(current-lastUploaded) / elapsed)
			}
			outputMu.Lock()
			printUploadProgress(output, name, total, current, speed)
			outputMu.Unlock()
			lastUploaded = current
			lastReport = now
		}
	}
}

func printUploadProgress(output io.Writer, name string, total, uploaded, speed int64) {
	percent := 100.0
	remaining := int64(0)
	if total > 0 {
		percent = float64(uploaded) * 100 / float64(total)
		if percent > 100 {
			percent = 100
		}
		remaining = total - uploaded
		if remaining < 0 {
			remaining = 0
		}
	}
	_, _ = fmt.Fprintf(output, "Upload progress [%s]: %.1f%% | %s/%s uploaded | %s remaining | %s/s\n",
		name, percent, formatBytes(uploaded), formatBytes(total), formatBytes(remaining), formatBytes(speed))
}

func formatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	value := float64(bytes)
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	for _, unitName := range units {
		value /= unit
		if value < unit {
			return fmt.Sprintf("%.1f %s", value, unitName)
		}
	}
	return fmt.Sprintf("%.1f PiB", value/unit)
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
