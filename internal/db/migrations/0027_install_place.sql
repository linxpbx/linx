-- "Moved to a new place?" (docs/INSTALL.md §8). Every control-plane start
-- writes where this server is into install_place, so it travels in every
-- backup. When a start finds a place written by another server (a restore
-- onto a new one), it keeps both in place_move, and the admin home lists
-- what in the backup may still point at the old place.

-- One row: this install's place, as the last start saw it.
CREATE TABLE install_place (
    id             boolean PRIMARY KEY DEFAULT true CHECK (id),
    -- setup's own id for this server (LINX_SERVER_ID; '' before it had one).
    server_id      text NOT NULL DEFAULT '',
    domain         text NOT NULL DEFAULT '',
    -- The phone networks from setup, e.g. {192.168.1.0/24}; {} on a
    -- rented server.
    lan_networks   text[] NOT NULL DEFAULT '{}',
    lan_address    text NOT NULL DEFAULT '',
    -- '' when the start couldn't tell.
    public_address text NOT NULL DEFAULT '',
    front_door     text NOT NULL DEFAULT '',
    recorded_at    timestamptz NOT NULL
);

-- Moves found at start, newest last. The admin home shows the newest one
-- until every item is done (done_at).
CREATE TABLE place_move (
    id           uuid PRIMARY KEY,
    detected_at  timestamptz NOT NULL,
    -- The places, as install_place's columns in JSON.
    before       jsonb NOT NULL,
    after        jsonb NOT NULL,
    -- Items ticked by hand: {"old_server": true, ...}.
    ticks        jsonb NOT NULL DEFAULT '{}',
    hidden_until timestamptz,
    done_at      timestamptz
);
