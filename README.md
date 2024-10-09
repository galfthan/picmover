# PicMover - Media Import and Organization Tool

## Overview

PicMover is a robust command-line tool designed to efficiently import, organize, and manage large collections of images and videos. It's built in Go and offers features like duplicate detection, metadata extraction, and intelligent file organization.

## Features

- **Intelligent File Organization**: Automatically organizes files based on their creation date and file type.
- **Duplicate Detection**: Uses hash-based comparison to prevent duplicate imports.
- **Metadata Extraction**: Extracts and stores EXIF data for images, including camera information and GPS coordinates.
- **Support for Various File Types**: Handles different image formats (JPEG, PNG, TIFF) and RAW files (CR2, DNG, etc.), as well as common video formats.
- **Database Management**: Uses SQLite to maintain a record of all imported files for quick access and management.
- **Performance Optimized**: Designed to handle large collections with hundreds of thousands of files efficiently.

## Installation

### Prerequisites

- Go 1.18 or higher
- SQLite3
- ExifTool (for extended metadata extraction)

### Steps

1. Clone the repository:
   ```
   git clone https://github.com/yourusername/picmover.git
   ```

2. Navigate to the project directory:
   ```
   cd picmover
   ```

3. Build the application:
   ```
   go build
   ```

## Usage

### Basic Import

To import media from a source directory to a destination directory:

```
./picmover import /path/to/source /path/to/destination
```

### Database Query

To query the database for information about imported files:

```
./picmover db /path/to/destination
```

### Additional Options

- Use `--min-dimension` to set a minimum dimension for imported images.
- Use `--limit` with the `db` command to control the number of entries displayed.

## Configuration

(If you implement a configuration file, provide details on how to set it up and what options are available)

## Notes

- The application creates a `media.db` file in the destination directory to store file information.
- RAW files are stored separately from standard image files for easier management.
- The import process can be safely interrupted and resumed.

## Limitations

- Video metadata extraction requires FFmpeg to be installed on the system.
- The application does not modify or edit the original files; it only copies them to the new location.

## Contributing

Contributions to PicMover are welcome! Please feel free to submit pull requests, create issues or spread the word.

## License

PicMover is free software: you can redistribute it and/or modify it under the terms of the GNU General Public License as published by the Free Software Foundation, either version 3 of the License, or (at your option) any later version.

This program is distributed in the hope that it will be useful, but WITHOUT ANY WARRANTY; without even the implied warranty of MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the GNU General Public License for more details.

You should have received a copy of the GNU General Public License along with this program. If not, see https://www.gnu.org/licenses/.

### What this means:

You are free to use, modify, and distribute this software.
If you distribute this software or any derivative works, you must do so under the GPL v3 license.
You must provide the source code for any distributed versions or modifications.
There is no warranty for this program.

For more details on the GPL v3 license, please visit: https://www.gnu.org/licenses/gpl-3.0.en.html

## Contact

Sebastian von Alfthan
