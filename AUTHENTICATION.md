# Google Drive authentication setup

This application uses Google OAuth 2.0 to access Drive on behalf of a Google
user. Setup requires a one-time browser sign-in and consent. Once a refresh
token has been saved, normal sync runs renew access tokens silently.

## 1. Create a Google Cloud project and enable Drive API

1. Open the [Google Cloud Console](https://console.cloud.google.com/), create or
   select a project, and enable the [Google Drive API](https://console.cloud.google.com/apis/library/drive.googleapis.com).
2. Open **Google Auth Platform** and configure the app's branding and audience.
   For a personal account, choose **External**. A Google Workspace administrator
   may instead choose **Internal** if the app is only for users in that
   organization.
3. While the app is in **Testing**, add the Google account that will authorize
   Drive under **Audience → Test users**. Use that same account during the
   authorization step below.

## 2. Create OAuth client credentials

1. In Google Auth Platform, open **Clients** and select **Create client**.
2. Choose **Desktop app**, give it a name, and create it.
3. Copy the client ID and client secret into the `oauth` object in your local
   `config.json`.

The app uses the Drive scope `https://www.googleapis.com/auth/drive`, which
allows it to list folders and create files and directories. Google may require
verification before an External app can be used broadly outside its test users.

## 3. Create the local config

Copy the example file and edit it:

```sh
cp config.example.json config.json
```

Set `local_dir` to the directory to upload, `drive_folder_name` to the name of
the destination folder directly under **My Drive**, and fill in `oauth.client_id`
and `oauth.client_secret`. Leave `oauth.refresh_token` empty for now. If the
name matches multiple folders in the Drive root, use `drive_folder_id` instead.

`config.json` is excluded by `.gitignore`; do not commit or share it. It contains
credentials and, after authorization, a long-lived refresh token.

## 4. Authorize and save the refresh token

Run this on a machine where you can sign in to the Google account and open a
browser:

```sh
go run . -config config.json -authorize
```

Sign in as a configured test user and grant the requested Drive access. The
application starts a temporary callback listener on localhost, receives the
authorization response, and writes the refresh token into `config.json`.
If the browser does not open automatically, visit the authorization URL printed
in the terminal. The callback listener waits up to five minutes.

The tool will report success when the token has been saved. If Google does not
return a refresh token, remove the app's existing access from your Google
Account's third-party connections and run the authorization command again.

## 5. Run sync

After authorization, use the normal command:

```sh
go run . -config config.json
```

This run does not open a browser; it uses the saved refresh token to obtain an
access token. A local file is removed only after Drive confirms its upload.

### Token lifetime and recovery

For an External app whose publishing status is **Testing**, Google generally
expires refresh tokens after seven days when the app requests Drive access.
If a token expires, add the account to the test users if necessary and repeat
the authorization command. Production use outside the test-user list may
require publishing the app and completing Google's verification for its
requested scopes. Refresh tokens can also be revoked by the user or expire for
other Google-documented reasons.

For Google's current requirements, see the
[OAuth consent screen setup](https://developers.google.com/identity/oauth2/web/guides/overview),
[OAuth for desktop apps](https://developers.google.com/identity/protocols/oauth2/native-app),
and [OAuth 2.0 refresh-token documentation](https://developers.google.com/identity/protocols/oauth2#expiration).
