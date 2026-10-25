package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"googledrivesync/internal/syncer"
)

func main() {
	configPath := flag.String("config", "config.json", "path to the JSON configuration file")
	authorize := flag.Bool("authorize", false, "authorize Google Drive access and save a refresh token")
	quiet := flag.Bool("quiet", false, "suppress per-file upload progress output")
	flag.Parse()

	ctx := context.Background()
	if *authorize {
		oauthConfig, err := syncer.LoadOAuthConfig(*configPath)
		if err != nil {
			log.Fatal(err)
		}
		if err := syncer.Authorize(ctx, oauthConfig, *configPath); err != nil {
			log.Fatal(err)
		}
		fmt.Println("Refresh token saved to config.")
		return
	}

	cfg, err := syncer.LoadConfig(*configPath)
	if err != nil {
		log.Fatal(err)
	}

	client, err := syncer.NewDriveClient(ctx, cfg.OAuth)
	if err != nil {
		log.Fatal(err)
	}

	if cfg.DriveFolderName != "" {
		err = syncer.SyncWithOptions(ctx, client, cfg.LocalDir, cfg.DriveFolderName, syncer.SyncOptions{Quiet: *quiet})
	} else {
		err = syncer.SyncByIDWithOptions(ctx, client, cfg.LocalDir, cfg.DriveFolderID, syncer.SyncOptions{Quiet: *quiet})
	}
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("Sync completed.")
}

func init() {
	log.SetFlags(0)
	log.SetOutput(os.Stderr)
}
