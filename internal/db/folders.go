package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	// foreignKeyViolation is the PostgreSQL SQLSTATE for a foreign-key breach.
	foreignKeyViolation = "23503"

	// folderParentFKey is PostgreSQL's default name for folders.parent_id's
	// foreign key.
	folderParentFKey = "folders_parent_id_fkey"
)

var (
	// ErrInvalidParentFolder means the requested parent does not exist or
	// belongs to a different project. Folders never cross projects.
	ErrInvalidParentFolder = errors.New("parent folder does not exist in this project")

	// ErrFolderCycle means a move would put a folder inside itself or inside
	// one of its own descendants.
	ErrFolderCycle = errors.New("a folder cannot be moved into itself or its own subfolder")
)

// MoveFolder re-parents a folder, with parentID nil meaning the project root.
// A nil sortOrder appends the folder after its new siblings.
//
// The whole check-then-write runs under a per-project lock: two concurrent
// moves (A into B, B into A) would otherwise each pass the cycle check and
// together detach both folders from the tree.
func (s *Store) MoveFolder(ctx context.Context, id uuid.UUID, parentID *uuid.UUID, sortOrder *int32) (Folder, error) {
	var moved Folder

	err := s.inTx(ctx, func(qtx *Queries) error {
		folder, err := qtx.GetFolderByID(ctx, id)
		if err != nil {
			// pgx.ErrNoRows travels unwrapped: deleted since the caller looked.
			return err
		}

		if err = qtx.LockProject(ctx, folder.ProjectID); err != nil {
			return fmt.Errorf("lock project: %w", err)
		}

		if parentID != nil {
			if err = requireFolderInProject(ctx, qtx, *parentID, folder.ProjectID, ErrInvalidParentFolder); err != nil {
				return err
			}

			var inSubtree bool
			inSubtree, err = qtx.IsDescendant(ctx, IsDescendantParams{
				FolderID: *parentID,
				RootID:   id,
			})
			if err != nil {
				return fmt.Errorf("check folder cycle: %w", err)
			}
			if inSubtree {
				return ErrFolderCycle
			}
		}

		moved, err = qtx.UpdateFolderParent(ctx, UpdateFolderParentParams{
			ID:        id,
			ParentID:  parentID,
			SortOrder: sortOrder,
		})
		if err != nil {
			return fmt.Errorf("update folder parent: %w", err)
		}

		return nil
	})
	if err != nil {
		return Folder{}, err
	}

	return moved, nil
}

// ReorderFolders rewrites sort_order for the children of parentID (nil = the
// project root), in the order given. checkSiblings receives the current sibling
// ids, read under lock, and vetoes the write by returning an error, which is
// passed back unchanged.
func (s *Store) ReorderFolders(
	ctx context.Context,
	projectID uuid.UUID,
	parentID *uuid.UUID,
	folderIDs []uuid.UUID,
	checkSiblings func(siblings []uuid.UUID) error,
) error {
	return s.inTx(ctx, func(qtx *Queries) error {
		if err := qtx.LockProject(ctx, projectID); err != nil {
			return fmt.Errorf("lock project: %w", err)
		}

		if parentID != nil {
			if err := requireFolderInProject(ctx, qtx, *parentID, projectID, ErrInvalidParentFolder); err != nil {
				return err
			}
		}

		siblings, err := qtx.ListSiblingFolderIDs(ctx, ListSiblingFolderIDsParams{
			ProjectID: projectID,
			ParentID:  parentID,
		})
		if err != nil {
			return fmt.Errorf("list sibling folders: %w", err)
		}

		if err := checkSiblings(siblings); err != nil {
			return err
		}

		if err := qtx.BulkUpdateFolderOrder(ctx, BulkUpdateFolderOrderParams{
			ProjectID: projectID,
			ParentID:  parentID,
			FolderIds: folderIDs,
		}); err != nil {
			return fmt.Errorf("update folder order: %w", err)
		}

		return nil
	})
}

// requireFolderInProject returns notInProject unless folderID is a folder of
// projectID. Callers pass the error that names their field (a folder's
// parent, a request's folder).
func requireFolderInProject(ctx context.Context, q *Queries, folderID, projectID uuid.UUID, notInProject error) error {
	folder, err := q.GetFolderByID(ctx, folderID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return notInProject
		}
		return fmt.Errorf("look up folder: %w", err)
	}

	if folder.ProjectID != projectID {
		return notInProject
	}

	return nil
}

// CreateFolderInProject creates a folder after checking that its parent, if
// any, is a folder of the same project; otherwise ErrInvalidParentFolder.
func (s *Store) CreateFolderInProject(ctx context.Context, arg CreateFolderParams) (Folder, error) {
	if arg.ParentID != nil {
		if err := requireFolderInProject(ctx, s.Queries, *arg.ParentID, arg.ProjectID, ErrInvalidParentFolder); err != nil {
			return Folder{}, err
		}
	}

	folder, err := s.CreateFolder(ctx, arg)
	if err != nil {
		// The parent was deleted between the check and the insert.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == foreignKeyViolation && pgErr.ConstraintName == folderParentFKey {
			return Folder{}, ErrInvalidParentFolder
		}
		return Folder{}, fmt.Errorf("create folder: %w", err)
	}

	return folder, nil
}
