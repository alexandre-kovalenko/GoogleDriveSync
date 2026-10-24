package syncer

import (
	"context"
	"fmt"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
)

func NewDriveClient(ctx context.Context, cfg OAuthConfig) (*drive.Service, error) {
	oauthConfig := &oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		Endpoint:     google.Endpoint,
		Scopes:       []string{drive.DriveScope},
	}
	token := &oauth2.Token{RefreshToken: cfg.RefreshToken}
	httpClient := oauthConfig.Client(ctx, token)

	service, err := drive.NewService(ctx, option.WithHTTPClient(httpClient))
	if err != nil {
		return nil, fmt.Errorf("create Google Drive client: %w", err)
	}
	return service, nil
}
