package cmd

import (
    "image"
    _ "image/jpeg"
    _ "image/png"
    "io"
    "os"
    "os/exec"
    "path/filepath"
    "time"
    "strings"
    "fmt"
    "regexp"
    "encoding/json"
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

func logMediaMetadata(path string, metadata MediaMetadata ) (error) {
    logger.Printf("Info: EXIF for %s: DateTime %s Location %s CameraModel %s CameraMake %s CameraType %s FileType %s Resolution %s \n", 
         path, metadata.DateTime, metadata.Location, metadata.CameraModel, metadata.CameraMake, metadata.CameraType, metadata.FileType, metadata.Resolution);
    return nil

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

    if fileType == "image" || fileType == "image_raw" {
        x, err := exif.Decode(file)
        if err != nil {
            logger.Printf("Warning: Could not read full EXIF data for %s (has some content %t): %v\n", path, x!=nil, err)
            // Even if full EXIF decoding fails, try to read individual fields
        }
        
        if x != nil {

            metadata.DateTime, err = getExifDateTime(x)
            if err != nil {
                logger.Printf("Warning: Could not read DateTime from EXIF for %s: %v\n", path, err)
            }
            
            metadata.Location, err = getExifLocation(x)
            if err != nil {
                logger.Printf("Warning: Could not read Location from EXIF for %s: %v\n", path, err)
            }
            
            metadata.CameraModel, err = getExifTag(x, exif.Model)
            if err != nil {
                logger.Printf("Warning: Could not read CameraModel from EXIF for %s: %v\n", path, err)
            }
            
            metadata.CameraMake, err = getExifTag(x, exif.Make)
            if err != nil {
                logger.Printf("Warning: Could not read CameraMake from EXIF for %s: %v\n", path, err)
            }
            
            metadata.CameraType = determineCameraType(metadata.CameraModel, metadata.CameraMake)
        }

        // For standard image files, try to get resolution from image 
        if fileType == "image" {
            _, err = file.Seek(0, 0)
            if err != nil {
                logger.Printf("Warning: Could not seek file %s: %v\n", path, err)
            }
            metadata.Resolution, err = getImageResolution(path)
            if err != nil {
                logger.Printf("Warning: Could not get resolution from image for %s: %v\n", path, err)
                if x != nil {
                    metadata.Resolution, err = getExifResolution(x)
                    if err != nil {
                        logger.Printf("Warning: Could not get resolution from EXIF for %s: %v\n", path, err)
                    }
                }
            }
        } else { // fileType == "image_raw"
            if x != nil {
                metadata.Resolution, err = getExifResolution(x)
                if err != nil {
                    logger.Printf("Warning: Could not get resolution from EXIF for RAW file %s: %v\n", path, err)
                }
            }
        }

        // If we still don't have a resolution, set a default value
        if metadata.Resolution == "" {
            logger.Printf("Warning: Could not determine resolution for %s\n", path)
            metadata.Resolution = "unknown"
        }

        // If DateTime is not set, fall back to file modification time
        if metadata.DateTime.IsZero() {
            metadata.DateTime, err = getModificationTime(file)
            if err != nil {
                logger.Printf("Warning: Could not get modification time for %s: %v\n", path, err)
            }
        }
    } else if fileType == "video" {
        metadata, err = getVideoMetadata(path)
        if err != nil {
            return MediaMetadata{}, fmt.Errorf("failed to extract video metadata: %w", err)
        }
    }

  //  logMediaMetadata(path, metadata)
    return metadata, nil
}


func getImageResolution(path string) (string, error) {
    file, err := os.Open(path)
    if err != nil {
        return "", err
    }
    defer file.Close()

    img, _, err := image.DecodeConfig(file)
    if err != nil {
        return "", err
    }

    return fmt.Sprintf("%dx%d", img.Width, img.Height), nil
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
    if x == nil {
        return "", fmt.Errorf("nil EXIF data")
    }
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

    // Known phone manufacturers and keywords
    phoneManufacturers := []string{
        "apple", "samsung", "huawei", "xiaomi", "oppo", "vivo", "oneplus", "lg", 
        "motorola", "nokia", "sony", "htc", "google", "asus", "lenovo", "alcatel",
        "zte", "blackberry", "meizu", "realme", "hmd global",
    }

    phoneKeywords := []string{
        "iphone", "android", "smartphone", "phone", "galaxy", "pixel", "xperia",
        "redmi", "poco", "mi ", "honor",
    }

    // Known camera manufacturers
    cameraManufacturers := []string{
        "canon", "nikon", "sony", "fujifilm", "olympus", "panasonic", "leica",
        "hasselblad", "pentax", "kodak",
    }

    // Check for phone manufacturers
    for _, manufacturer := range phoneManufacturers {
        if strings.Contains(lowerMake, manufacturer) {
            return "phone"
        }
    }

    // Check for phone keywords
    for _, keyword := range phoneKeywords {
        if strings.Contains(lowerModel, keyword) || strings.Contains(lowerMake, keyword) {
            return "phone"
        }
    }

    // Check for camera manufacturers
    for _, manufacturer := range cameraManufacturers {
        if strings.Contains(lowerMake, manufacturer) {
            return "camera"
        }
    }

    // Check for specific model patterns
    if matched, _ := regexp.MatchString(`^(SM-|LG-|XT\d{4}|FRD-|LE\d{4}|AC\d{4})`, model); matched {
        return "phone"
    }

    // If we can't determine, return "unknown"
    return "unknown"
}
func isMediaFile(path string) (fileType string, isMedia bool) {
    ext := strings.ToLower(filepath.Ext(path))
    switch ext {
    case ".jpg", ".jpeg", ".png", ".tiff", ".tif":
        return "image", true
    case ".cr2", ".crw", ".cr3", ".dng", ".nef", ".arw", ".x3f", ".orf", ".rw2", ".raf", ".pef", ".srw", ".raw":
        return "image_raw", true
    case ".mp4", ".mpg", ".mpeg", ".mov", ".avi", ".mkv", ".flv", ".3gp", ".wmv", ".mts", ".m2ts", ".ts", ".webm", ".asf", ".m4v":
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

type FFProbeOutput struct {
    Streams []struct {
        CodecType string `json:"codec_type"`
        Width     int    `json:"width"`
        Height    int    `json:"height"`
        Tags      struct {
            CreationTime string `json:"creation_time"`
        } `json:"tags"`
    } `json:"streams"`
    Format struct {
        Filename string `json:"filename"`
        Tags     struct {
            CreationTime             string `json:"creation_time"`
            Software                 string `json:"software"`
            Location                 string `json:"location"`
            LocationEng              string `json:"location-eng"`
            AndroidVersion           string `json:"com.android.version"`
            AndroidCaptureFPS        string `json:"com.android.capture.fps"`
            AppleQuicktimeMake       string `json:"com.apple.quicktime.make"`
            AppleQuicktimeModel      string `json:"com.apple.quicktime.model"`
            AppleQuicktimeLocationDate string `json:"com.apple.quicktime.location.date"`
            AppleQuicktimeLocationISO6709 string `json:"com.apple.quicktime.location.ISO6709"`
        } `json:"tags"`
    } `json:"format"`
}
func getVideoMetadata(path string) (MediaMetadata, error) {
    metadata := MediaMetadata{
        FileType: "video",
    }

    cmd := exec.Command("ffprobe",
        "-v", "quiet",
        "-print_format", "json",
        "-show_format",
        "-show_streams",
        path)

    output, err := cmd.Output()
    if err != nil {
        return metadata, fmt.Errorf("ffprobe failed: %w", err)
    }

    var ffprobeData FFProbeOutput
    if err := json.Unmarshal(output, &ffprobeData); err != nil {
        return metadata, fmt.Errorf("failed to parse ffprobe output: %w", err)
    }

    // Extract resolution
    for _, stream := range ffprobeData.Streams {
        if stream.CodecType == "video" {
            metadata.Resolution = fmt.Sprintf("%dx%d", stream.Width, stream.Height)
            break
        }
    }

    // Extract creation time
    creationTime := ffprobeData.Format.Tags.CreationTime
    if creationTime == "" {
        creationTime = ffprobeData.Format.Tags.AppleQuicktimeLocationDate
    }
    if creationTime != "" {
        // Try parsing with multiple formats
        formats := []string{
            "2006-01-02T15:04:05.000000Z",
            "2006-01-02T15:04:05Z",
            "2006-01-02 15:04:05",
            time.RFC3339Nano,
            "2006-01-02T15:04:05.999999999Z07:00",
        }
        for _, format := range formats {
            if t, err := time.Parse(format, creationTime); err == nil {
                metadata.DateTime = t
                break
            }
        }
    }

    // If DateTime is still not set, fall back to file modification time
    if metadata.DateTime.IsZero() {
        file, err := os.Open(path)
        if err == nil {
            metadata.DateTime, _ = getModificationTime(file)
            file.Close()
        }
    }

    // Extract location
    location := ffprobeData.Format.Tags.Location
    if location == "" {
        location = ffprobeData.Format.Tags.LocationEng
    }
    if location == "" {
        location = ffprobeData.Format.Tags.AppleQuicktimeLocationISO6709
    }
    if location != "" {
        metadata.Location = strings.TrimSuffix(location, "/")
    }

    // Check for camera information
    if ffprobeData.Format.Tags.AppleQuicktimeMake != "" {
        metadata.CameraMake = ffprobeData.Format.Tags.AppleQuicktimeMake
        metadata.CameraModel = ffprobeData.Format.Tags.AppleQuicktimeModel
        metadata.CameraType = "camera"
    } else if ffprobeData.Format.Tags.AndroidVersion != "" {
        metadata.CameraModel = fmt.Sprintf("Android %s", ffprobeData.Format.Tags.AndroidVersion)
        metadata.CameraMake = "Android"
        metadata.CameraType = "phone"
        if fps := ffprobeData.Format.Tags.AndroidCaptureFPS; fps != "" {
            metadata.CameraModel += fmt.Sprintf(" (FPS: %s)", fps)
        }
    }  else if ffprobeData.Format.Tags.Software != "" {
        if strings.HasPrefix(ffprobeData.Format.Tags.Software, "Canon") {
            metadata.CameraMake = "Canon"
            metadata.CameraModel = ffprobeData.Format.Tags.Software
            metadata.CameraType = "camera"
        } else {
            metadata.CameraModel = ffprobeData.Format.Tags.Software
        } 
    }    
    
    return metadata, nil
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