package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
)

// webhookSlugKey is the UNIQUE constraint on webhook_endpoints.slug, whose
// violation means a random slug collided and should be regenerated.
const webhookSlugKey = "webhook_endpoints_slug_key"

// ErrWebhookQuota means the project already has the maximum number of webhook
// endpoints.
var ErrWebhookQuota = errors.New("webhook endpoint quota reached")

// CreateWebhookEndpointWithQuota creates an endpoint unless the project
// already has max of them (ErrWebhookQuota). The count and the insert run
// under the project lock, so two concurrent creates cannot both squeeze under
// the limit.
func (s *Store) CreateWebhookEndpointWithQuota(ctx context.Context, arg CreateWebhookEndpointParams, maxEndpoints int64) (WebhookEndpoint, error) {
	var endpoint WebhookEndpoint

	err := s.inTx(ctx, func(qtx *Queries) error {
		if err := qtx.LockProject(ctx, arg.ProjectID); err != nil {
			return fmt.Errorf("lock project: %w", err)
		}

		count, err := qtx.CountWebhookEndpointsInProject(ctx, arg.ProjectID)
		if err != nil {
			return fmt.Errorf("count webhook endpoints: %w", err)
		}
		if count >= maxEndpoints {
			return ErrWebhookQuota
		}

		endpoint, err = qtx.CreateWebhookEndpoint(ctx, arg)
		if err != nil {
			// Wrapped with %w, so IsWebhookSlugCollision still sees the PgError.
			return fmt.Errorf("create webhook endpoint: %w", err)
		}

		return nil
	})
	if err != nil {
		return WebhookEndpoint{}, err
	}

	return endpoint, nil
}

// IsWebhookSlugCollision reports a slug that already exists.
func IsWebhookSlugCollision(err error) bool {
	return isConstraintViolation(err, uniqueViolation, webhookSlugKey)
}

// CaptureWebhook stores one captured request and, in the same transaction,
// prunes the endpoint down to its newest keep captures. It returns the new
// capture's id.
func (s *Store) CaptureWebhook(ctx context.Context, arg InsertWebhookCaptureParams, keep int32) (int64, error) {
	var id int64

	err := s.inTx(ctx, func(qtx *Queries) error {
		var err error
		id, err = qtx.InsertWebhookCapture(ctx, arg)
		if err != nil {
			return fmt.Errorf("insert webhook capture: %w", err)
		}

		if err = qtx.PruneWebhookCaptures(ctx, PruneWebhookCapturesParams{
			EndpointID:       arg.EndpointID,
			OldestKeptOffset: keep - 1,
		}); err != nil {
			return fmt.Errorf("prune webhook captures: %w", err)
		}

		return nil
	})
	if err != nil {
		return 0, err
	}

	return id, nil
}

// uniqueViolation is the PostgreSQL SQLSTATE for a unique-constraint breach.
const uniqueViolation = "23505"

// isConstraintViolation reports a PostgreSQL error with the given SQLSTATE on
// the named constraint.
func isConstraintViolation(err error, code, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == code && pgErr.ConstraintName == constraint
}
