package db

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// Variable types. These mirror the environment_variables_type CHECK
// constraint; changing one means changing the other in a migration.
const (
	VariableRegular = "regular"
	VariableSecret  = "secret"
)

// ReorderEnvironments rewrites the order of a project's environments. check
// receives the current ids, read under lock, and vetoes the write by returning
// an error, which is passed back unchanged.
func (s *Store) ReorderEnvironments(
	ctx context.Context,
	projectID uuid.UUID,
	environmentIDs []uuid.UUID,
	check func(current []uuid.UUID) error,
) error {
	return s.inTx(ctx, func(qtx *Queries) error {
		current, err := qtx.ListEnvironmentIDsForUpdate(ctx, projectID)
		if err != nil {
			return fmt.Errorf("list environments: %w", err)
		}

		if err := check(current); err != nil {
			return err
		}

		if err := qtx.BulkUpdateEnvironmentOrder(ctx, BulkUpdateEnvironmentOrderParams{
			ProjectID:      projectID,
			EnvironmentIds: environmentIDs,
		}); err != nil {
			return fmt.Errorf("update environment order: %w", err)
		}

		return nil
	})
}

// ReorderVariables rewrites the order of an environment's variables, with the
// same locked check-then-write as ReorderEnvironments.
func (s *Store) ReorderVariables(
	ctx context.Context,
	environmentID uuid.UUID,
	variableIDs []uuid.UUID,
	check func(current []uuid.UUID) error,
) error {
	return s.inTx(ctx, func(qtx *Queries) error {
		current, err := qtx.ListVariableIDsForUpdate(ctx, environmentID)
		if err != nil {
			return fmt.Errorf("list variables: %w", err)
		}

		if err := check(current); err != nil {
			return err
		}

		if err := qtx.BulkUpdateVariableOrder(ctx, BulkUpdateVariableOrderParams{
			EnvironmentID: environmentID,
			VariableIds:   variableIDs,
		}); err != nil {
			return fmt.Errorf("update variable order: %w", err)
		}

		return nil
	})
}
