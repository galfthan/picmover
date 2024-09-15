package cmd

import (
    "io"
    "os"
    "path/filepath"
    "time"
    "strings"
    "fmt"
    "github.com/cespare/xxhash"
    "github.com/rwcarlsen/goexif/exif"
    "github.com/rwcarlsen/goexif/mknote"
   
)

func init() {
    // Register the mknote parsers to handle a wider range of EXIF tags
    exif.RegisterParsers(mknote.All...)
}

func isMediaFile(path string) (string, bool) {
    ext := strings.ToLower(filepath.Ext(path))
    switch ext {
    case ".jpg", ".jpeg", ".png", ".tiff", ".tif", ".cr2":
        return "image", true
    case ".mp4", ".mov", ".avi", ".mkv", ".flv", ".wmv":
        return "video", true
    default:
        return "", false
    }
}
func getModificationTime(file *os.File) (time.Time, error) {
    // Get file modification time 
    fileInfo, err := file.Stat()
    if err != nil {
        return time.Time{}, err
    }
    return fileInfo.ModTime(), nil
    
}


func getMediaDateTime(path string) (time.Time, error) {
    file, err := os.Open(path)
    if err != nil {
        return time.Time{}, fmt.Errorf("failed to open file: %w", err)
    }
    defer file.Close()

    fileType, isMedia := isMediaFile(path)
    if !isMedia {
        return time.Time{}, fmt.Errorf("not a supported media file")
    }

    if fileType == "image" {
        return getExifDateTime(file)
    } else {
        // For video files, use file modification time
        return getModificationTime(file)
    }
}


func getExifDateTime(file *os.File) (time.Time, error) {
    x, err := exif.Decode(file)
    if err != nil {
        // If the image is missing EXIF or EXIF decoding fails, we revert to modification time
        return getModificationTime(file)
    }
   
    for _, tag := range []string{"DateTimeOriginal", "CreateDate", "DateTime", "ModifyDate"} {
        dt, err := x.Get(exif.FieldName(tag))
        if err == nil {
            str, err := dt.StringVal()
            if err == nil {
                t, err := parseExifDate(str)
                if err == nil {
                    return t, nil
                }
            }
        }
    }

    // If no valid EXIF date is found, fall back to modification time
    return getModificationTime(file)
}


func parseExifDate(date string) (time.Time, error) {
    layouts := []string{
        "2006:01:02 15:04:05",
        "2006:01:02 15:04:05-07:00",
        "2006-01-02 15:04:05",
        "2006-01-02T15:04:05",
        "2006-01-02T15:04:05-07:00",
    }

    for _, layout := range layouts {
        if t, err := time.Parse(layout, date); err == nil {
            return t, nil
        }
    }

    return time.Time{}, fmt.Errorf("unable to parse date: %s", date)
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