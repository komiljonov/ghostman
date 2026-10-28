package db

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// MaxExampleBodySize caps a saved example's body. Examples sync to every
// member's client, hence the hard cap; it mirrors the
// request_examples_body_size CHECK.
const MaxExampleBodySize = 1 << 20

// ReorderRequestExamples rewrites the order of a request's examples. check
// receives the current ids, read under lock, and vetoes the write by returning
// an error, which is passed back unchanged.
func (s *Store) ReorderRequestExamples(
	ctx context.Context,
	requestID uuid.UUID,
	exampleIDs []uuid.UUID,
	check func(current []uuid.UUID) error,
) error {
	return s.inTx(ctx, func(qtx *Queries) error {
		current, err := qtx.ListRequestExampleIDsForUpdate(ctx, requestID)
		if err != nil {
			return fmt.Errorf("list examples: %w", err)
		}

		if err := check(current); err != nil {
			return err
		}

		if err := qtx.BulkUpdateRequestExampleOrder(ctx, BulkUpdateRequestExampleOrderParams{
			RequestID:  requestID,
			ExampleIds: exampleIDs,
		}); err != nil {
			return fmt.Errorf("update example order: %w", err)
		}

		return nil
	})
}

const exampleRequestFKey = "request_examples_request_id_fkey"

// IsExampleRequestGone reports an example insert whose request was deleted
// between the caller's access check and the write.
func IsExampleRequestGone(err error) bool {
	return isConstraintViolation(err, foreignKeyViolation, exampleRequestFKey)
}
