# Google Drive Sync

A command-line Go application that uploads regular files from a local directory
to a Google Drive folder specified by its name in the Drive root. Nested directories are recreated below the destination
folder. A local file is removed only after Drive confirms the upload; files whose
upload fails are left in place. The application continues with other files and
reports per-file failures at the end. Symbolic links and non-regular files are
skipped.

## Configure

1. Enable the Google Drive API in a Google Cloud project and create OAuth client
   credentials.
2. Create a **Desktop app** OAuth client in Google Cloud and put its client ID
   and secret in the config. Enable the Google Drive API for that project.
3. Copy `config.example.json` to `config.json` and set the local directory,
   destination folder name in `drive_folder_name` (the folder must be directly
   under My Drive's root), client ID, and client secret. Keep this file private
   and out of source control. If multiple root folders have that name, the app
   reports an ambiguity; use `drive_folder_id` instead in that case.

Run the one-time authorization command on a machine with a browser and access to
the Google account that can use the destination folder:

```sh
go run . -config config.json -authorize
```

It opens Google's consent page, listens only on a temporary loopback port, and
saves the returned refresh token to `config.json`. Google account sign-in and
consent are required; subsequent normal sync runs use the saved token silently.
Keep the config private because it contains a long-lived credential.

The configured OAuth client must have the `drive` scope authorized so it can
access the destination folder and create files and subfolders there.

## Run

```sh
go run . -config config.json
```

The config path defaults to `config.json`. Files are uploaded recursively while
retaining their relative directory structure, with up to four files uploading
concurrently. Progress for active uploads is reported every 30 seconds with
file name, percentage, bytes uploaded and remaining, and upload speed. Empty
directories are also created in Drive; local directories are not removed.
Existing configurations with `drive_folder_id` remain supported.
