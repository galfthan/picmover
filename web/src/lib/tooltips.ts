// Tooltip texts used in more than one place, so the safety wording of an
// action is the same wherever it is offered (issue #23). Each says what the
// action does to the files and whether it can be undone; see
// docs/design/management.md (trash) and docs/design/library-move.md (move).

/** Trash: the file goes to <library>/.picmover/trash; Restore brings it back. */
export const TRASH_TITLE = 'Move to the trash. It can be restored from the Trash page until the trash is emptied.';

/** Delete permanently (of trashed items). */
export const DELETE_TITLE = 'Delete from the disk. This cannot be undone.';

/** Restore (of trashed items): ratings, tags and albums were kept while in the trash. */
export const RESTORE_TITLE = 'Put back in the library at its old path (or a free name beside it), with its rating, tags and albums.';

/** Move to another library. */
export const MOVE_TITLE =
  'Move to another library, with ratings, tags and albums. Nothing is deleted: when the target is on another drive, the originals go to this library’s trash.';

/** Empty trash. */
export const EMPTY_TRASH_TITLE = 'Delete every file in the trash from the disk. This cannot be undone; you type the number of files to confirm.';

/** Where ratings, tags and albums live. */
export const USERDATA_TITLE = 'Kept in the library’s user.db. The file itself is not changed.';

/** Import: Copy. */
export const IMPORT_COPY_TITLE = 'Copy each file into the library. Nothing on the source (card, phone or folder) is changed or deleted.';

/**
 * Import: Move. The importer removes a source file only after its catalog
 * batch commits; duplicates, skipped files and entries of zip archives are
 * never removed (internal/importer, deleteSrc).
 */
export const IMPORT_MOVE_TITLE =
  'Delete each source file (on the card, phone or folder) once it is in the library. Duplicates, small images, non-media files (such as sidecars) and zip archives stay on the source. The deletion cannot be undone.';
