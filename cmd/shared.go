package cmd

import (
    "io"
    "os"
    "path/filepath"
    "time"

    "github.com/cespare/xxhash"
    "github.com/rwcarlsen/goexif/exif"
)

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