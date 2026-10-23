package db

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// Proxy types and node proxy modes. They mirror the proxies_proxy_type and
// <table>_proxy_mode CHECK constraints.
const (
	ProxyTypeHTTP   = "http"
	ProxyTypeHTTPS  = "https"
	ProxyTypeSOCKS5 = "socks5"

	ProxyModeInherit = "inherit"
	ProxyModeGlobal  = "global"
	ProxyModeNone    = "none"
	ProxyModeProxy   = "proxy"
)

// ProxyTypes and ProxyModes list every value, in the order error messages
// name them.
var (
	ProxyTypes = []string{ProxyTypeHTTP, ProxyTypeHTTPS, ProxyTypeSOCKS5}
	ProxyModes = []string{ProxyModeInherit, ProxyModeGlobal, ProxyModeNone, ProxyModeProxy}
)

// Constraint names the API maps to client errors.
const (
	proxyNameKey     = "proxies_project_name_idx"
	requestProxyFKey = "requests_proxy_id_fkey"
	folderProxyFKey  = "folders_proxy_id_fkey"
)

// IsProxyNameTaken reports a proxy name already used in the project (case
// insensitive).
func IsProxyNameTaken(err error) bool {
	return isConstraintViolation(err, uniqueViolation, proxyNameKey)
}

// IsProxyGone reports a node update naming a proxy that was deleted between
// the caller's check and the write.
func IsProxyGone(err error) bool {
	return isConstraintViolation(err, foreignKeyViolation, requestProxyFKey) ||
		isConstraintViolation(err, foreignKeyViolation, folderProxyFKey)
}

// ReorderProxies rewrites the order of a project's proxies. check receives the
// current ids, read under lock, and vetoes the write by returning an error,
// which is passed back unchanged.
func (s *Store) ReorderProxies(
	ctx context.Context,
	projectID uuid.UUID,
	proxyIDs []uuid.UUID,
	check func(current []uuid.UUID) error,
) error {
	return s.inTx(ctx, func(qtx *Queries) error {
		current, err := qtx.ListProxyIDsForUpdate(ctx, projectID)
		if err != nil {
			return fmt.Errorf("list proxies: %w", err)
		}

		if err := check(current); err != nil {
			return err
		}

		if err := qtx.BulkUpdateProxyOrder(ctx, BulkUpdateProxyOrderParams{
			ProjectID: projectID,
			ProxyIds:  proxyIDs,
		}); err != nil {
			return fmt.Errorf("update proxy order: %w", err)
		}

		return nil
	})
}
