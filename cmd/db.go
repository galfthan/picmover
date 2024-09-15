package cmd

import (
    "database/sql"
    "fmt"

    "path/filepath"


    "github.com/spf13/cobra"
    _ "github.com/mattn/go-sqlite3"
)


var (
    listFiles bool
    limit     int
)

var dbCmd = &cobra.Command{
    Use:   "db [destination_directory]",
    Short: "Query the image database",
    Long:  `Query and display information from the image database.`,
    Args:  cobra.ExactArgs(1),
    Run: func(cmd *cobra.Command, args []string) {
        destDir := args[0]
        queryDatabase(destDir)
    },
}

func init() {
    rootCmd.AddCommand(dbCmd)
    dbCmd.Flags().BoolVarP(&listFiles, "list", "l", false, "List files in the database")
    dbCmd.Flags().IntVarP(&limit, "limit", "n", 10, "Limit the number of files to display (default 100, use 0 for no limit)")
}

func queryDatabase(destDir string) {
    dbPath := filepath.Join(destDir, "media.db")
    db, err := sql.Open("sqlite3", dbPath)
    if err != nil {
        fmt.Printf("Error opening database: %v\n", err)
        return
    }
    defer db.Close()

    if listFiles {
        displayFileList(db)
    } else {
        displaySummary(db)
        displayRecentFiles(db)
    }
}
func displaySummary(db *sql.DB) {
    rows, err := db.Query(`
        SELECT 
            COUNT(*) as total,
            SUM(CASE WHEN file_type = 'image' THEN 1 ELSE 0 END) as images,
            SUM(CASE WHEN file_type = 'video' THEN 1 ELSE 0 END) as videos,
            MIN(date_taken) as earliest,
            MAX(date_taken) as latest,
            COUNT(DISTINCT camera_model) as unique_cameras,
            COUNT(DISTINCT camera_make) as unique_makes,
            COUNT(DISTINCT CASE WHEN location != '' THEN location END) as locations_with_gps
        FROM media
    `)
    if err != nil {
        fmt.Printf("Error querying database: %v\n", err)
        return
    }
    defer rows.Close()

    if rows.Next() {
        var total, images, videos, uniqueCameras, uniqueMakes, locationsWithGPS int
        var earliest, latest string
        err := rows.Scan(&total, &images, &videos, &earliest, &latest, &uniqueCameras, &uniqueMakes, &locationsWithGPS)
        if err != nil {
            fmt.Printf("Error scanning row: %v\n", err)
            return
        }
        fmt.Printf("Database Summary:\n")
        fmt.Printf("Total files: %d\n", total)
        fmt.Printf("Images: %d\n", images)
        fmt.Printf("Videos: %d\n", videos)
        fmt.Printf("Date range: %s to %s\n", earliest, latest)
        fmt.Printf("Unique camera models: %d\n", uniqueCameras)
        fmt.Printf("Unique camera makes: %d\n", uniqueMakes)
        fmt.Printf("Files with GPS data: %d\n", locationsWithGPS)
    }
}


func displayRecentFiles(db *sql.DB) {
    fmt.Printf("\nMost Recent Files:\n")
    query := `
        SELECT new_path, date_taken, file_type
        FROM media
        ORDER BY date_taken DESC
        LIMIT ?
    `
    rows, err := db.Query(query, limit)
    if err != nil {
        fmt.Printf("Error querying recent files: %v\n", err)
        return
    }
    defer rows.Close()

    for rows.Next() {
        var path, dateTaken, fileType string
        err := rows.Scan(&path, &dateTaken, &fileType)
        if err != nil {
            fmt.Printf("Error scanning recent row: %v\n", err)
            continue
        }
        fmt.Printf("%s - %s (%s)\n", dateTaken, filepath.Base(path), fileType)
    }
}
func displayFileList(db *sql.DB) {
    query := `
        SELECT id, hash, original_path, new_path, date_taken, file_type, location, camera_model, camera_make, camera_type, resolution
        FROM media
        ORDER BY date_taken DESC
    `
    var rows *sql.Rows
    var err error
    
    if limit > 0 {
        query += " LIMIT ?"
        rows, err = db.Query(query, limit)
    } else {
        rows, err = db.Query(query)
    }

    if err != nil {
        fmt.Printf("Error querying files: %v\n", err)
        return
    }
    defer rows.Close()

    fmt.Println("File List:")
    fmt.Println("ID | Hash | Original Path | New Path | Date Taken | File Type | Location | Camera Model | Camera Make | Camera Type | Resolution")
    fmt.Println("-------------------------------------------------------------------------------------------------------------------")

    count := 0
    for rows.Next() {
        var id int
        var hash int64
        var originalPath, newPath, dateTaken, fileType, location, cameraModel, cameraMake, cameraType, resolution string
        err := rows.Scan(&id, &hash, &originalPath, &newPath, &dateTaken, &fileType, &location, &cameraModel, &cameraMake, &cameraType, &resolution)
        if err != nil {
            fmt.Printf("Error scanning row: %v\n", err)
            continue
        }
        fmt.Printf("%d | %d | %s | %s | %s | %s | %s | %s | %s | %s | %s\n",
            id, hash, originalPath, newPath, dateTaken, fileType, location, cameraModel, cameraMake, cameraType, resolution)
        count++
    }

    fmt.Printf("\nTotal files displayed: %d\n", count)
}
