package supervision

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// MarkDigestItemDelivered advances one preflight digest row to `seen`, skipping
// the writes the row already has.
//
// Preflight used to re-mark every item on every parent turn, and each write
// bumps version/updated_at. The 2026-09-16 audit (plan §4.3) found a single
// stale stalled row churned past version 180 while nothing about the underlying
// condition had changed — the churn also invalidates the CAS version a caller
// (an ack/close issued right after reading a snapshot) had just observed, so a
// user-visible retry is spent on a no-op refresh. Delivery state is monotonic
// (pending → delivered → seen), so re-issuing a transition the row already
// carries can never change the outcome.
func MarkDigestItemDelivered(ctx context.Context, store Store, item DigestItem, at time.Time) error {
	if store == nil || strings.TrimSpace(item.NotificationID) == "" {
		return nil
	}
	if item.DeliveryState == DeliverySeen {
		return nil
	}
	if item.DeliveryState != DeliveryDelivered {
		if err := store.MarkNotificationDelivered(ctx, item.NotificationID, at); err != nil {
			return fmt.Errorf("mark supervision notification delivered: %w", err)
		}
	}
	if err := store.MarkNotificationSeen(ctx, item.NotificationID, at); err != nil {
		return fmt.Errorf("mark supervision notification seen: %w", err)
	}
	return nil
}
