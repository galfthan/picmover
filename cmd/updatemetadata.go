package cmd

import (
    "database/sql"
    "fmt"
    "os"
    "path/filepath"


    "github.com/spf13/cobra"
    _ "github.com/mattn/go-sqlite3"
)

var updateMetadataCmd = &cobra.Command{
    Use:   "update-metadata [archive_directory]",
    Short: "Update metadata in the database from media files",
    Long:  `Scan through all media files in the database and update their metadata based on the current file content.`,
    Args:  cobra.ExactArgs(1),
    Run: func(cmd *cobra.Command, args []string) {
        destDir := args[0]
        updateDatabaseMetadata(destDir)
    },
}

var (
    updateType string
    dryRun     bool
)

func init() {
    rootCmd.AddCommand(updateMetadataCmd)
    updateMetadataCmd.Flags().StringVarP(&updateType, "type", "t", "all", "Type of media to update (all, video, image, or image_raw)")
	updateMetadataCmd.Flags().BoolVarP(&dryRun, "dry-run", "d", false, "Perform a dry run without making any changes")
}

func updateDatabaseMetadata(destDir string) {
    dbPath := filepath.Join(destDir, "media.db")
    db, err := sql.Open("sqlite3", dbPath)
    if err != nil {
        fmt.Printf("Error opening database: %v\n", err)
        return
    }
    defer db.Close()

    query := `SELECT id, new_path, file_type, date_taken, location, camera_model, camera_make, camera_type, resolution FROM media`
    if updateType != "all" {
        query += fmt.Sprintf(" WHERE file_type = '%s'", updateType)
    }

    rows, err := db.Query(query)
    if err != nil {
        fmt.Printf("Error querying database: %v\n", err)
        return
    }
    defer rows.Close()

    var updated, errors, unchanged int

    for rows.Next() {
        var id int
        var oldMetadata MediaMetadata
        var newPath, fileType string
        err := rows.Scan(&id, &newPath, &fileType, &oldMetadata.DateTime, &oldMetadata.Location, &oldMetadata.CameraModel, &oldMetadata.CameraMake, &oldMetadata.CameraType, &oldMetadata.Resolution)
        if err != nil {
            fmt.Printf("Error scanning row: %v\n", err)
            errors++
            continue
        }

        if _, err := os.Stat(newPath); os.IsNotExist(err) {
            fmt.Printf("File not found: %s\n", newPath)
            errors++
            continue
        }

        newMetadata, err := getMediaMetadata(newPath)
        if err != nil {
            fmt.Printf("Error getting metadata for %s: %v\n", newPath, err)
            errors++
            continue
        }

        changes := compareMetadata(oldMetadata, newMetadata)
        if len(changes) > 0 {
            if dryRun {
                fmt.Printf("Would update %s:\n", newPath)
            } else {
                err = updateMediaRecord(db, id, newMetadata)
                if err != nil {
                    fmt.Printf("Error updating record for %s: %v\n", newPath, err)
                    errors++
                    continue
                }
                fmt.Printf("Updated %s:\n", newPath)
            }
            for _, change := range changes {
                fmt.Printf("  %s\n", change)
            }
            updated++
        } else {
            unchanged++
        }
    }

    if dryRun {
        fmt.Printf("Dry run complete. Would update: %d, Unchanged: %d, Errors: %d\n", updated, unchanged, errors)
    } else {
        fmt.Printf("Update complete. Updated: %d, Unchanged: %d, Errors: %d\n", updated, unchanged, errors)
    }
}


func compareMetadata(old, new MediaMetadata) []string {
    var changes []string
    if !old.DateTime.Equal(new.DateTime) {
        changes = append(changes, fmt.Sprintf("Date/Time: %v -> %v", old.DateTime, new.DateTime))
    }
    if old.Location != new.Location {
        changes = append(changes, fmt.Sprintf("Location: %s -> %s", old.Location, new.Location))
    }
    if old.CameraModel != new.CameraModel {
        changes = append(changes, fmt.Sprintf("Camera Model: %s -> %s", old.CameraModel, new.CameraModel))
    }
    if old.CameraMake != new.CameraMake {
        changes = append(changes, fmt.Sprintf("Camera Make: %s -> %s", old.CameraMake, new.CameraMake))
    }
    if old.CameraType != new.CameraType {
        changes = append(changes, fmt.Sprintf("Camera Type: %s -> %s", old.CameraType, new.CameraType))
    }
    if old.Resolution != new.Resolution {
        changes = append(changes, fmt.Sprintf("Resolution: %s -> %s", old.Resolution, new.Resolution))
    }
    return changes
}


func updateMediaRecord(db *sql.DB, id int, metadata MediaMetadata) error {
    _, err := db.Exec(`
        UPDATE media 
        SET date_taken = ?, location = ?, camera_model = ?, camera_make = ?, camera_type = ?, resolution = ?
        WHERE id = ?`,
        metadata.DateTime, metadata.Location, metadata.CameraModel, metadata.CameraMake, metadata.CameraType, metadata.Resolution, id)
    return err
}