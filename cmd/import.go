package cmd

import (
    "database/sql"
    "fmt"
    "io"
    "os"
    "os/signal"  
    "context" 
    "strings"
    "path/filepath"
    "sync"
    "syscall"
    "time"
    "github.com/spf13/cobra"
    _ "github.com/mattn/go-sqlite3"
    "archive/zip"

)

type ImportResult struct {
    Status      string  // "imported", "skipped_in_db", "skipped_not_in_db", "error"
    Message     string
    OriginalPath string
    NewPath      string
    InDatabase    bool
}

var importCmd = &cobra.Command{
    Use:   "import [source_directory] [destination_directory]",
    Short: "Import and organize images into a new directory structure",
    Long:  `Import images from the source directory and organize them into a new directory structure based on their EXIF date.`,
    Args:  cobra.ExactArgs(2),
    Run: func(cmd *cobra.Command, args []string) {
        sourceDir := args[0]
        destDir := args[1]
        importImages(sourceDir, destDir)
    },
}
var (
    minDimension int
)
func init() {  
   rootCmd.AddCommand(importCmd)
   importCmd.Flags().IntVar(&minDimension, "min-dimension", 0, "Minimum dimension (width or height) for imported images. 0 means no limit.")
}

func importImages(sourceDir, destDir string) {
    db, err := initDB(destDir)
    if err != nil {
        fmt.Printf("Error initializing database: %v\n", err)
        return
    }
    defer db.Close()

    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()

    // Set up signal handling to catch ctrl+c and sigterm
    sigChan := make(chan os.Signal, 1)
    signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
    
    var wg sync.WaitGroup
    wg.Add(1)

    go func() {
        defer wg.Done()
        select {
        case <-sigChan:
            fmt.Println("\nReceived interrupt signal. Cancelling import...")
            cancel()
        case <-ctx.Done():
        }
    }()


    var stats struct {
        Imported       int
        SkippedInDB    int
        SkippedNotInDB int
        SkippedSmall   int
        Errors         int
    }

    err = filepath.Walk(sourceDir, func(path string, info os.FileInfo, err error) error {
        if err != nil {
            return err
        }

        select {
        case <-ctx.Done():
            return context.Canceled
        default:
            if info.IsDir() {
                return nil
            }

            if filepath.Ext(path) == ".zip" {
                err = processZipFile(path, destDir, db, &stats)
                if err != nil {
                    fmt.Printf("Error processing zip file %s: %v\n", path, err)
                    stats.Errors++
                }
            } else {
                processFile(path, destDir, db, &stats)
            }

            return nil
        }
    })

    if err != nil {
        if err == context.Canceled {
            fmt.Println("Import cancelled.")
        } else {
            fmt.Printf("Error walking through directory: %v\n", err)
        }
    }

 
    fmt.Printf("\nImport Summary:\n")
    fmt.Printf("Imported: %d\n", stats.Imported)
    fmt.Printf("Skipped (in DB): %d\n", stats.SkippedInDB)
    fmt.Printf("Skipped (not in DB): %d\n", stats.SkippedNotInDB)
    fmt.Printf("Skipped (too small): %d\n", stats.SkippedSmall)
    fmt.Printf("Errors: %d\n", stats.Errors)
}

func processZipFile(zipPath, destDir string, db *sql.DB, stats *struct {
    Imported       int
    SkippedInDB    int
    SkippedNotInDB int
    SkippedSmall   int
    Errors         int
}) error {
    reader, err := zip.OpenReader(zipPath)
    if err != nil {
        return err
    }
    defer reader.Close()
    // Use the zip file name as the temp folder name
    zipBaseName := filepath.Base(zipPath)
    tempDir, err := os.MkdirTemp("", fmt.Sprintf("%s_", zipBaseName))

    if err != nil {
        return fmt.Errorf("failed to create temp directory: %w", err)
    }
    defer os.RemoveAll(tempDir) // Clean up temp directory when done

    for _, file := range reader.File {
        if file.FileInfo().IsDir() {
            continue
        }

        err := extractAndProcessFile(file, tempDir, destDir, db, stats)
        if err != nil {
            fmt.Printf("Error processing file %s from zip: %v\n", file.Name, err)
            stats.Errors++
        }
    }

    return nil
}


func extractAndProcessFile(file *zip.File, tempDir, destDir string, db *sql.DB, stats *struct {
    Imported       int
    SkippedInDB    int
    SkippedNotInDB int
    SkippedSmall   int
    Errors         int
}) error {
    // Create a temporary file with the original name
    tempFilePath := filepath.Join(tempDir, filepath.Base(file.Name))
    tempFile, err := os.Create(tempFilePath)
    if err != nil {
        return fmt.Errorf("failed to create temp file: %w", err)
    }
    defer tempFile.Close()
    defer os.Remove(tempFilePath) // Clean up temp file when done

    // Extract the file
    zippedFile, err := file.Open()
    if err != nil {
        return fmt.Errorf("failed to open zipped file: %w", err)
    }
    defer zippedFile.Close()

    _, err = io.Copy(tempFile, zippedFile)
    if err != nil {
        return fmt.Errorf("failed to extract file: %w", err)
    }

    // Ensure all data is written to disk
    err = tempFile.Sync()
    if err != nil {
        return fmt.Errorf("failed to sync temp file: %w", err)
    }

    // Close the file to ensure we can modify its timestamps
    tempFile.Close()

    // Set the modification time of the temporary file to match the original file in the ZIP
    err = os.Chtimes(tempFilePath, time.Now(), file.Modified)
    if err != nil {
        return fmt.Errorf("failed to set file times: %w", err)
    }

    // Process the extracted file
    result := processAndMoveMedia(tempFilePath, destDir, db)
    updateStats(result, stats)

    return nil
}

func updateStats(result ImportResult, stats *struct {
    Imported       int
    SkippedInDB    int
    SkippedNotInDB int
    SkippedSmall   int
    Errors         int
}) {
    switch result.Status {
    case "imported":
        fmt.Printf("Imported: %s -> %s\n", result.OriginalPath, result.NewPath)
        stats.Imported++
    case "skipped_in_db":
        fmt.Printf("Skipped (in DB): %s (%s)\n", result.OriginalPath, result.Message)
        stats.SkippedInDB++
    case "skipped_not_in_db":
        fmt.Printf("Skipped (not in DB): %s (%s)\n", result.OriginalPath, result.Message)
        stats.SkippedNotInDB++
    case "skipped_small":
        fmt.Printf("Skipped (too small): %s (%s)\n", result.OriginalPath, result.Message)
        stats.SkippedSmall++
    case "error":
        fmt.Printf("Error processing %s: %s\n", result.OriginalPath, result.Message)
        stats.Errors++
    }
}

func processFile(path, destDir string, db *sql.DB, stats *struct {
    Imported       int
    SkippedInDB    int
    SkippedNotInDB int
    SkippedSmall   int
    Errors         int
}) {
    if _, isMedia := isMediaFile(path); isMedia {
        result := processAndMoveMedia(path, destDir, db)
        updateStats(result, stats)
    }
}


func processAndMoveMedia(sourcePath, destDir string, db *sql.DB) ImportResult {
    fileType, isMedia := isMediaFile(sourcePath)
    if !isMedia {
        return ImportResult{Status: "error", Message: "Not a supported media file", OriginalPath: sourcePath}
    }

    metadata, err := getMediaMetadata(sourcePath)
    if err != nil {
        return ImportResult{Status: "error", Message: fmt.Sprintf("Error reading metadata: %v", err), OriginalPath: sourcePath}
    }

    // Check dimensions for images
    if fileType == "image" && minDimension > 0 {
        width, height, err := parseResolution(metadata.Resolution)
        if err != nil {
            return ImportResult{Status: "error", Message: fmt.Sprintf("Error parsing resolution: %v", err), OriginalPath: sourcePath}
        }
        if width < minDimension && height < minDimension {
            return ImportResult{
                Status:       "skipped_small",
                Message:      fmt.Sprintf("Image dimensions (%dx%d) smaller than minimum (%dx%d)", width, height, minDimension, minDimension),
                OriginalPath: sourcePath,
            }
        }
    }
   
    hash, err := computeXXHash(sourcePath)
    if err != nil {
        return ImportResult{Status: "error", Message: fmt.Sprintf("Error computing hash: %v", err), OriginalPath: sourcePath}
    }
    isDuplicate, existingPath, err := checkDuplicate(db, hash)
    if err != nil {
        return ImportResult{Status: "error", Message: fmt.Sprintf("Error checking for duplicates: %v", err), OriginalPath: sourcePath}
    }
    if isDuplicate {
        return ImportResult{
            Status:       "skipped_in_db",
            Message:      fmt.Sprintf("Duplicate media found in database. Hash: %x, Existing file: %s", hash, existingPath),
            OriginalPath: sourcePath,
            InDatabase:   true,
        }
    }
  

    newPath := generateNewPath(sourcePath, metadata.DateTime, destDir, fileType)
    
    if _, err := os.Stat(newPath); err == nil {
        existingHash, err := computeXXHash(newPath)
        if err != nil {
            return ImportResult{Status: "error", Message: fmt.Sprintf("Error computing hash of existing file: %v", err), OriginalPath: sourcePath}
        }
        
        if hash != existingHash {
            newPath = generateUniqueFilename(newPath)
        } else {
            return ImportResult{Status: "skipped_not_in_db", Message: "Identical file already exists at destination but not in database", OriginalPath: sourcePath, NewPath: newPath, InDatabase: false}
        }
    }

    if err := copyFile(sourcePath, newPath); err != nil {
        return ImportResult{Status: "error", Message: fmt.Sprintf("Error copying file: %v", err), OriginalPath: sourcePath}
    }

    if err := storeInDB(db, hash, sourcePath, newPath, metadata); err != nil {
        return ImportResult{Status: "error", Message: fmt.Sprintf("Error storing in database: %v", err), OriginalPath: sourcePath, NewPath: newPath}
    }

    return ImportResult{Status: "imported", Message: "File successfully imported", OriginalPath: sourcePath, NewPath: newPath}
}


func parseResolution(resolution string) (int, int, error) {
    var width, height int
    _, err := fmt.Sscanf(resolution, "%dx%d", &width, &height)
    if err != nil {
        return 0, 0, err
    }
    return width, height, nil
}

func generateUniqueFilename(path string) string {
    dir, file := filepath.Split(path)
    ext := filepath.Ext(file)
    name := strings.TrimSuffix(file, ext)
    
    counter := 1
    newPath := path
    for {
        if _, err := os.Stat(newPath); os.IsNotExist(err) {
            // File doesn't exist, we can use this name
            return newPath
        }
        // File exists, try the next number
        newPath = filepath.Join(dir, fmt.Sprintf("%s_%d%s", name, counter, ext))
        counter++
    }
}
func initDB(destDir string) (*sql.DB, error) {
    dbPath := filepath.Join(destDir, "media.db")
    db, err := sql.Open("sqlite3", dbPath)
    if err != nil {
        return nil, fmt.Errorf("error opening database: %w", err)
    }

    _, err = db.Exec(`
    CREATE TABLE IF NOT EXISTS media (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        hash INTEGER UNIQUE,
        original_path TEXT,
        new_path TEXT,
        date_taken DATETIME,
        file_type TEXT,
        location TEXT,
        camera_model TEXT,
        camera_make TEXT,
        camera_type TEXT,
        resolution TEXT
    )`)
    if err != nil {
        db.Close()
        return nil, fmt.Errorf("error creating table: %w", err)
    }

    return db, nil
}


func storeInDB(db *sql.DB, hash uint64, originalPath, newPath string, metadata MediaMetadata) error {
    _, err := db.Exec(`
        INSERT INTO media (hash, original_path, new_path, date_taken, file_type, location, camera_model, camera_make, camera_type, resolution) 
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
        int64(hash), originalPath, newPath, metadata.DateTime, metadata.FileType, metadata.Location, metadata.CameraModel, metadata.CameraMake, metadata.CameraType, metadata.Resolution)
    return err
}

func checkDuplicate(db *sql.DB, hash uint64) (bool, string, error) {
    var existingPath string
    err := db.QueryRow("SELECT new_path FROM media WHERE hash = ?", int64(hash)).Scan(&existingPath)
    if err == sql.ErrNoRows {
        return false, "", nil
    }
    if err != nil {
        return false, "", err
    }
    return true, existingPath, nil
}



func generateNewPath(sourcePath string, dateTime time.Time, destDir string, fileType string) string {
    fileName := filepath.Base(sourcePath)
    return filepath.Join(destDir, fileType, dateTime.Format("2006"), dateTime.Format("01"), fileName)
}


func copyFile(src, dst string) error {
    sourceFile, err := os.Open(src)
    if err != nil {
        return err
    }
    defer sourceFile.Close()

    // Get file information
    sourceInfo, err := sourceFile.Stat()
    if err != nil {
        return err
    }

    // Ensure the destination directory exists
    err = os.MkdirAll(filepath.Dir(dst), os.ModePerm)
    if err != nil {
        return err
    }

    // Create the destination file
    destFile, err := os.Create(dst)
    if err != nil {
        return err
    }
    defer destFile.Close()

    // Copy the contents
    _, err = io.Copy(destFile, sourceFile)
    if err != nil {
        return err
    }

    // Sync to ensure write is complete
    err = destFile.Sync()
    if err != nil {
        return err
    }

    // Close the destination file before setting times
    destFile.Close()

    // Preserve modification time
    err = os.Chtimes(dst, sourceInfo.ModTime(), sourceInfo.ModTime())
    if err != nil {
        return err
    }

    

    return nil
}

