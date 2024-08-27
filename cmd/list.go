package cmd

import (
    "fmt"
    "io"
    "os"
    "path/filepath"
    "time"
    "github.com/cespare/xxhash"
    "github.com/rwcarlsen/goexif/exif"
    "github.com/spf13/cobra"
)

// listCmd represents the list command
var listCmd = &cobra.Command{
    Use:   "list [directory]",
    Short: "List all images with their EXIF date",
    Long:  `List all images in the specified directory along with their EXIF date (DateTimeOriginal).`,
    Args:  cobra.ExactArgs(1),
    Run: func(cmd *cobra.Command, args []string) {
        directory := args[0]
        listImages(directory)
    },
}

func init() {
    rootCmd.AddCommand(listCmd)
}

// listImages lists all images in the directory with their EXIF date
func listImages(directory string) {
    err := filepath.Walk(directory, func(path string, info os.FileInfo, err error) error {
        if err != nil {
            return err
        }

        if !info.IsDir() && isImageFile(path) {
            dateTime, err := getExifDateTime(path)
            if err != nil {
                fmt.Printf("Error reading EXIF data from %s: %v\n", path, err)
            } else {
                hash, err := computeXXHash(path)
                if err != nil {
                    fmt.Printf("Error computing xxHash for %s: %v\n", path, err)
                } else {
                    fmt.Printf("Date: %s, xxHash: %016x, File: %s\n", dateTime.Format(time.RFC3339), hash, path)
                }
            }

          
        }

        return nil
    })

    if err != nil {
        fmt.Printf("Error walking through directory: %v\n", err)
    }
}

func isImageFile(path string) bool {
    ext := filepath.Ext(path)
    switch ext {
    case ".jpg", ".jpeg", ".png", ".tiff", ".tif":
        return true
    default:
        return false
    }
}

func getExifDateTime(path string) (time.Time, error) {
    file, err := os.Open(path)
    if err != nil {
        return time.Time{}, err
    }
    defer file.Close()

    x, err := exif.Decode(file)
    if err != nil {
        return time.Time{}, err
    }

    dateTime, err := x.DateTime()
    if err != nil {
        return time.Time{}, err
    }

    return dateTime, nil
}

func computeXXHash(path string) (uint64, error) {
    file, err := os.Open(path)
    if err != nil {
        return 0, err
    }
    defer file.Close()

    hash := xxhash.New()
    if _, err := io.Copy(hash, file); err != nil {
        return 0, err
    }

    return hash.Sum64(), nil
}
