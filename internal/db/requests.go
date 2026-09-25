package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

// Request methods. These mirror the requests_method CHECK constraint; changing
// one means changing the other in a migration.
var RequestMethods = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"}

// requestFolderFKey is PostgreSQL's default name for requests.folder_id's
// foreign key.
const requestFolderFKey = "requests_folder_id_fkey"

// ErrInvalidRequestFolder means the requested folder does not exist or belongs
// to a different project. Requests never cross projects.
var ErrInvalidRequestFolder = errors.New("folder_id is not a folder of this project")

// CreateRequestInProject creates a request after checking that its folder, if
// any, belongs to the same project; otherwise ErrInvalidRequestFolder.
func (s *Store) CreateRequestInProject(ctx context.Context, arg CreateRequestParams) (Request, error) {
	if arg.FolderID != nil {
		if err := requireFolderInProject(ctx, s.Queries, *arg.FolderID, arg.ProjectID, ErrInvalidRequestFolder); err != nil {
			return Request{}, err
		}
	}

	request, err := s.CreateRequest(ctx, arg)
	if err != nil {
		if isRequestFolderGone(err) {
			return Request{}, ErrInvalidRequestFolder
		}
		return Request{}, fmt.Errorf("create request: %w", err)
	}

	return request, nil
}

// MoveRequest puts a request into folderID, with nil meaning the project root.
// A nil sortOrder appends it after its new siblings. Requests are leaves, so
// there is no cycle to check; the project lock only keeps the move from
// interleaving with a reorder of the same siblings.
func (s *Store) MoveRequest(ctx context.Context, id uuid.UUID, folderID *uuid.UUID, sortOrder *int32) (Request, error) {
	var moved Request

	err := s.inTx(ctx, func(qtx *Queries) error {
		request, err := qtx.GetRequestByID(ctx, id)
		if err != nil {
			// pgx.ErrNoRows travels unwrapped: deleted since the caller looked.
			return err
		}

		if err = qtx.LockProject(ctx, request.ProjectID); err != nil {
			return fmt.Errorf("lock project: %w", err)
		}

		if folderID != nil {
			if err = requireFolderInProject(ctx, qtx, *folderID, request.ProjectID, ErrInvalidRequestFolder); err != nil {
				return err
			}
		}

		moved, err = qtx.UpdateRequestFolder(ctx, UpdateRequestFolderParams{
			ID:        id,
			FolderID:  folderID,
			SortOrder: sortOrder,
		})
		if err != nil {
			if isRequestFolderGone(err) {
				return ErrInvalidRequestFolder
			}
			return fmt.Errorf("update request folder: %w", err)
		}

		return nil
	})
	if err != nil {
		return Request{}, err
	}

	return moved, nil
}

// ReorderRequests rewrites sort_order for the requests in folderID (nil = the
// project root), in the order given, under the project lock. checkSiblings
// receives the current sibling ids, read under lock, and vetoes the write by
// returning an error, which is passed back unchanged.
func (s *Store) ReorderRequests(
	ctx context.Context,
	projectID uuid.UUID,
	folderID *uuid.UUID,
	requestIDs []uuid.UUID,
	checkSiblings func(siblings []uuid.UUID) error,
) error {
	return s.inTx(ctx, func(qtx *Queries) error {
		if err := qtx.LockProject(ctx, projectID); err != nil {
			return fmt.Errorf("lock project: %w", err)
		}

		if folderID != nil {
			if err := requireFolderInProject(ctx, qtx, *folderID, projectID, ErrInvalidRequestFolder); err != nil {
				return err
			}
		}

		siblings, err := qtx.ListSiblingRequestIDs(ctx, ListSiblingRequestIDsParams{
			ProjectID: projectID,
			FolderID:  folderID,
		})
		if err != nil {
			return fmt.Errorf("list sibling requests: %w", err)
		}

		if err := checkSiblings(siblings); err != nil {
			return err
		}

		if err := qtx.BulkUpdateRequestOrder(ctx, BulkUpdateRequestOrderParams{
			ProjectID:  projectID,
			FolderID:   folderID,
			RequestIds: requestIDs,
		}); err != nil {
			return fmt.Errorf("update request order: %w", err)
		}

		return nil
	})
}

// isRequestFolderGone reports a folder deleted between the check and the write.
func isRequestFolderGone(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == foreignKeyViolation && pgErr.ConstraintName == requestFolderFKey
}
