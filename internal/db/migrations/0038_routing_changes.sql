-- Undo for call routing (ADR-071, docs/PHASE1F.md §9, Phase 1F step 16).
--
-- Every change to where calls go (what numbers and lines ring, ring
-- groups, office hours and holidays, "When someone calls", the order
-- outgoing calls try the lines and what each calling level allows) first
-- keeps the routing as it was just before, in the change's own
-- transaction (internal/store routing_changes.go). The last 50 are kept.
-- Putting one back is itself a change, so it can be undone too. Asterisk
-- never reads this table.
CREATE TABLE routing_change (
    id          uuid PRIMARY KEY,
    tenant_id   uuid NOT NULL REFERENCES tenant (id),
    at          timestamptz NOT NULL,
    -- Who made it, as in the activity log ("user:<id>").
    actor       text NOT NULL,
    action      text NOT NULL,
    target      text NOT NULL DEFAULT '',
    -- The routing tables' rows just before the change.
    before      jsonb NOT NULL,
    -- The routing before and after, in the screens' sentences
    -- (internal/routing changes.go), made in the change's transaction, so
    -- "See the change" shows exactly what it did. NULL when the server
    -- couldn't say (a tool that saves without the control plane).
    before_words jsonb,
    after_words  jsonb,
    -- A put-back: which change's "before" it put back, and that change's
    -- time (kept for the line's words once the change itself is pruned).
    put_back_of uuid,
    put_back_at timestamptz
);

CREATE INDEX routing_change_tenant_idx ON routing_change (tenant_id, at DESC, id DESC);
