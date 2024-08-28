package cmd

import (
    "database/sql"
    "fmt"
    "io"
    "os"
    "path/filepath"
    "time"
    "github.com/spf13/cobra"
    _ "github.com/mattn/go-sqlite3"

)

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
    db, err := initDB()
    if err != nil {
        fmt.Printf("Error initializing database: %v\n", err)
        return
    }
    defer db.Close()

    err = filepath.Walk(sourceDir, func(path string, info os.FileInfo, err error) error {
        if err != nil {
            return err
        }
        if !info.IsDir() && isImageFile(path) {
            if err := processAndMoveImage(path, destDir, db); err != nil {
                fmt.Printf("Error processing %s: %v\n", path, err)
            }
        }
        return nil
    })

    if err != nil {
        fmt.Printf("Error walking through directory: %v\n", err)
    }
}

func processAndMoveImage(sourcePath, destDir string, db *sql.DB) error {
    dateTime, err := getExifDateTime(sourcePath)
    if err != nil {
        return fmt.Errorf("error reading EXIF data: %v", err)
    }

    hash, err := computeXXHash(sourcePath)
    if err != nil {
        return fmt.Errorf("error computing hash: %v", err)
    }

    // Check for duplicates
    isDuplicate, err := checkDuplicate(db, hash)
    if err != nil {
        return fmt.Errorf("error checking for duplicates: %v", err)
    }
    if isDuplicate {
        return fmt.Errorf("duplicate image found")
    }

    newPath := generateNewPath(sourcePath, dateTime, destDir)
    if err := copyFile(sourcePath, newPath); err != nil {
        return fmt.Errorf("error copying file: %v", err)
    }

    if err := storeInDB(db, hash, sourcePath, newPath, dateTime); err != nil {
        return fmt.Errorf("error storing in database: %v", err)
    }

    fmt.Printf("Processed: %s -> %s\n", sourcePath, newPath)
    return nil
}

func initDB() (*sql.DB, error) {
    db, err := sql.Open("sqlite3", "images.db")
    if err != nil {
        return nil, err
    }

    _, err = db.Exec(`
    CREATE TABLE IF NOT EXISTS images (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        hash INTEGER UNIQUE,
        original_path TEXT,
        new_path TEXT,
        date_taken DATETIME
    )`)
    if err != nil {
        return nil, err
    }

    return db, nil
}

func checkDuplicate(db *sql.DB, hash uint64) (bool, error) {
    var count int
    err := db.QueryRow("SELECT COUNT(*) FROM images WHERE hash = ?", int64(hash)).Scan(&count)
    return count > 0, err
}

func generateNewPath(sourcePath string, dateTime time.Time, destDir string) string {
    fileName := filepath.Base(sourcePath)
    return filepath.Join(destDir, dateTime.Format("2006"), dateTime.Format("01"), fileName)
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

func storeInDB(db *sql.DB, hash uint64, originalPath, newPath string, dateTaken time.Time) error {
    _, err := db.Exec("INSERT INTO images (hash, original_path, new_path, date_taken) VALUES (?, ?, ?, ?)",
        int64(hash), originalPath, newPath, dateTaken)
    return err
}