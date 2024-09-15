package cmd

import (
    "database/sql"
    "fmt"
    "io"
    "os"
    "strings"
    "path/filepath"
    "time"
    "github.com/spf13/cobra"
    _ "github.com/mattn/go-sqlite3"

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

func init() {  
   rootCmd.AddCommand(importCmd)
}

func importImages(sourceDir, destDir string) {
    db, err := initDB(destDir)
    if err != nil {
        fmt.Printf("Error initializing database: %v\n", err)
        return
    }
    defer db.Close()

    var stats struct {
        Imported        int
        SkippedInDB     int
        SkippedNotInDB  int
        Errors          int
    }


    err = filepath.Walk(sourceDir, func(path string, info os.FileInfo, err error) error {
        if err != nil {
            return err
        }
        if !info.IsDir(){
            if _,ismedia:= isMediaFile(path); ismedia {
                result := processAndMoveMedia(path, destDir, db)
                switch result.Status {
                case  "imported":
                   fmt.Printf("Imported: %s -> %s\n", result.OriginalPath, result.NewPath)
                    stats.Imported++
                case "skipped_in_db":
                 fmt.Printf("Skipped (in DB): %s\n", result.OriginalPath)
                   stats.SkippedInDB++
                case "skipped_not_in_db":
                    fmt.Printf("Skipped (not in DB): %s (%s)\n", result.OriginalPath, result.Message)
                   stats.SkippedNotInDB++
                case "error":
                    fmt.Printf("Error processing %s: %s\n", result.OriginalPath, result.Message)
                  stats.Errors++
                }     
            }   
        }
        return nil
    })

    if err != nil {
        fmt.Printf("Error walking through directory: %v\n", err)
    }
    fmt.Printf("\nImport Summary:\n")
    fmt.Printf("Imported: %d\n", stats.Imported)
    fmt.Printf("Skipped (in DB): %d\n", stats.SkippedInDB)
    fmt.Printf("Skipped (not in DB): %d\n", stats.SkippedNotInDB)
    fmt.Printf("Errors: %d\n", stats.Errors)
}



func processAndMoveMedia(sourcePath, destDir string, db *sql.DB) ImportResult {
    fileType, isMedia := isMediaFile(sourcePath)
    if !isMedia {
        return ImportResult{Status: "error", Message: "Not a supported media file", OriginalPath: sourcePath}
    }

    dateTime, err := getMediaDateTime(sourcePath)
    if err != nil {
        return ImportResult{Status: "error", Message: fmt.Sprintf("Error reading metadata: %v", err), OriginalPath: sourcePath}
    }

    hash, err := computeXXHash(sourcePath)
    if err != nil {
        return ImportResult{Status: "error", Message: fmt.Sprintf("Error computing hash: %v", err), OriginalPath: sourcePath}
    }

    isDuplicate, err := checkDuplicate(db, hash)
    if err != nil {
        return ImportResult{Status: "error", Message: fmt.Sprintf("Error checking for duplicates: %v", err), OriginalPath: sourcePath}
    }
    if isDuplicate {
        return ImportResult{Status: "skipped_in_db", Message: "Duplicate media found in database", OriginalPath: sourcePath, InDatabase: true}
    }

    newPath := generateNewPath(sourcePath, dateTime, destDir, fileType)
    
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

    if err := storeInDB(db, hash, sourcePath, newPath, dateTime, fileType); err != nil {
        return ImportResult{Status: "error", Message: fmt.Sprintf("Error storing in database: %v", err), OriginalPath: sourcePath, NewPath: newPath}
    }

    return ImportResult{Status: "imported", Message: "File successfully imported", OriginalPath: sourcePath, NewPath: newPath}
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
        file_type TEXT
    )`)
    if err != nil {
        db.Close()
        return nil, fmt.Errorf("error creating table: %w", err)
    }

    return db, nil
}



func storeInDB(db *sql.DB, hash uint64, originalPath, newPath string, dateTaken time.Time, fileType string) error {
    _, err := db.Exec(`
        INSERT INTO media (hash, original_path, new_path, date_taken, file_type) 
        VALUES (?, ?, ?, ?, ?)`,
        int64(hash), originalPath, newPath, dateTaken, fileType)
    return err
}


func checkDuplicate(db *sql.DB, hash uint64) (bool, error) {
    var count int
    err := db.QueryRow("SELECT COUNT(*) FROM media WHERE hash = ?", int64(hash)).Scan(&count)
    return count > 0, err
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

    err = os.MkdirAll(filepath.Dir(dst), os.ModePerm)
    if err != nil {
        return err
    }

    destFile, err := os.Create(dst)
    if err != nil {
        return err
    }
    defer destFile.Close()

    _, err = io.Copy(destFile, sourceFile)
    return err
}

