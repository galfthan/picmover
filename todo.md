#Todo

1. restructure the processAndMoveMedia function to perform the hash check first:


2. Database Transactions: For database operations that involve multiple steps (like in processAndMoveMedia), consider using transactions to ensure atomicity.

3. Concurrency: The current implementation is single-threaded. If performance becomes an issue with large numbers of files, you might want to consider adding concurrency in the future.

4. bug: In processAndMoveMedia, if copyFile fails, you might want to delete the entry from the database to maintain consistency

5. Configuration: moving some of the hardcoded values (like supported file extensions in isMediaFile) to a configuration file for easier maintenance.


6. Adding a way to cleanly break the importing process. Can implement this using a combination of signal handling and a cancellation mechanism. 

7. Optimization: DB Indexing: Ensure your hash column is indexed for fast duplicate checks.

8. DB consistency: add missing files in destination to database, and remove from database files that have been deleted. 

9. Filemanagement: rm files from database and folders.

10. Monitoring: Implement some basic performance logging to track query times as your database grows.

11. add gui

12. Fix dng bug: Warning: Could not get resolution from EXIF for RAW file /mnt/d/images/2012/10/07/IMG_4508.dng: exif: tag "PixelXDimension" is not present

13. Optimize: minimize io, in memory image processing (read once)

14. Add sigma raw, x3f support

15. Use `filepath.WalkDir` instead of `filepath.Walk`:
      - `WalkDir` is more efficient as it doesn't call `os.Stat` for each file.

16. Preserve the file creation date during the copy process. Modify the copyFile function in the import.go file to preserve both the modification time and the creation time (where supported by the operating system). 
  => first check why some files have wrong date and some not


