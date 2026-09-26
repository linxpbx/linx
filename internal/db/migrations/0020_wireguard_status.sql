-- Phase 1D step 5 (docs/TRUNKS.md §7): each WireGuard tunnel's state, as
-- linx-wireguard last reported it (internal/wgconf), kept by the control
-- plane's trunk monitor. Not part of the profile's settings: a change
-- doesn't bump version (an admin's If-Match stays valid).
ALTER TABLE wireguard_profile
    ADD COLUMN status            text NOT NULL DEFAULT 'unknown'
        CHECK (status IN ('up', 'connecting', 'down', 'unknown')),
    ADD COLUMN status_detail     text NOT NULL DEFAULT '',
    ADD COLUMN status_since      timestamptz,
    ADD COLUMN last_handshake_at timestamptz;
