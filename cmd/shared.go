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
    exif.RegisterParsers(mknote.All...)
}

type MediaMetadata struct {
    DateTime     time.Time
    Location     string
    CameraModel  string
    CameraMake   string
    CameraType   string
    FileType     string
    Resolution   string
}

func getMediaMetadata(path string) (MediaMetadata, error) {
    file, err := os.Open(path)
    if err != nil {
        return MediaMetadata{}, fmt.Errorf("failed to open file: %w", err)
    }
    defer file.Close()

    fileType, isMedia := isMediaFile(path)
    if !isMedia {
        return MediaMetadata{}, fmt.Errorf("not a supported media file")
    }

    metadata := MediaMetadata{
        FileType: fileType,
    }

    if fileType == "image" {
        x, err := exif.Decode(file)
        if err == nil {
            metadata.DateTime, _ = getExifDateTime(x)
            metadata.Location, _ = getExifLocation(x)
            metadata.CameraModel, _ = getExifTag(x, exif.Model)
            metadata.CameraMake, _ = getExifTag(x, exif.Make)
            metadata.CameraType = determineCameraType(metadata.CameraModel, metadata.CameraMake)
            metadata.Resolution, _ = getExifResolution(x)
        } else {
            // If EXIF parsing fails, fall back to file info
            metadata.DateTime, _ = getModificationTime(file)
        }
    } else {
        // For video files, use file modification time and other file properties
        metadata.DateTime, _ = getModificationTime(file)
        // You might want to add video-specific metadata extraction here
    }

    return metadata, nil
}

func getExifDateTime(x *exif.Exif) (time.Time, error) {
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
    
    // If no valid date is found in any of the tags
    return time.Time{}, fmt.Errorf("no valid date found in EXIF")
}




func getExifLocation(x *exif.Exif) (string, error) {
    lat, long, err := x.LatLong()
    if err != nil {
        return "", err
    }
    return fmt.Sprintf("%.6f,%.6f", lat, long), nil
}
func getExifTag(x *exif.Exif, tag exif.FieldName) (string, error) {
    field, err := x.Get(tag)
    if err != nil {
        return "", err
    }
    str, err := field.StringVal()
    if err != nil {
        return "", err
    }
    return str, nil
}


func getExifResolution(x *exif.Exif) (string, error) {
    width, err := x.Get(exif.PixelXDimension)
    if err != nil {
        return "", err
    }
    height, err := x.Get(exif.PixelYDimension)
    if err != nil {
        return "", err
    }
    w, _ := width.Int(0)
    h, _ := height.Int(0)
    return fmt.Sprintf("%dx%d", w, h), nil
}

func determineCameraType(model, make string) string {
    lowerModel := strings.ToLower(model)
    lowerMake := strings.ToLower(make)
    
    phoneKeywords := []string{"iphone", "android", "smartphone", "phone"}
    for _, keyword := range phoneKeywords {
        if strings.Contains(lowerModel, keyword) || strings.Contains(lowerMake, keyword) {
            return "phone"
        }
    }
    return "camera"
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