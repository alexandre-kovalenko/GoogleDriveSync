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
		err = syncer.Sync(ctx, client, cfg.LocalDir, cfg.DriveFolderName)
	} else {
		err = syncer.SyncByID(ctx, client, cfg.LocalDir, cfg.DriveFolderID)
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
