-- Admin portal settings and "confirm it's you" (docs/ADMIN.md §4, §7,
-- Phase 1E step 2). The numbering plan (extension_digits, extension_ranges)
-- and the wizard's other settings live on pbx_setting, the same one-row
-- table 1D added country to.

ALTER TABLE pbx_setting
    -- How many digits an extension number has, and the plain ranges the
    -- numbering step draws as a picture (docs/ADMIN.md §4): a JSON array of
    -- {"kind": "people"|"groups"|"reserved", "from": 100, "to": 599}, from
    -- lowest to highest, never overlapping and never touching a reserved
    -- number (checked by the API, not here: it needs the numbering package's
    -- per-country data).
    ADD COLUMN extension_digits  smallint NOT NULL DEFAULT 3 CHECK (extension_digits BETWEEN 2 AND 6),
    ADD COLUMN extension_ranges  jsonb NOT NULL DEFAULT
        '[{"kind":"people","from":100,"to":599},{"kind":"groups","from":600,"to":699},{"kind":"reserved","from":700,"to":899}]',
    -- Asked in the web wizard, not linx setup (docs/ADMIN.md §4 item 5).
    ADD COLUMN site_kind         text NOT NULL DEFAULT '' CHECK (site_kind IN ('', 'home', 'business')),
    ADD COLUMN simple_mode       boolean NOT NULL DEFAULT true,
    -- Where admins may sign in from (docs/ADMIN.md §3): restricted false
    -- means "anywhere, with a second step" (the default); true means only
    -- from one of admin_networks. Never blocks API keys, which have their
    -- own allowed_ips.
    ADD COLUMN admin_network_restricted boolean NOT NULL DEFAULT false,
    ADD COLUMN admin_networks   cidr[] NOT NULL DEFAULT '{}',
    -- The "Everyone" call permission level the setup wizard creates and
    -- gives to every extension automatically, new ones too (docs/ADMIN.md
    -- §4). NULL until the wizard (or an admin) sets one: existing extensions
    -- with no level keep 1D's fail-closed behaviour (emergency only).
    ADD COLUMN default_call_permission_level_id uuid REFERENCES call_permission_level (id) ON DELETE SET NULL,
    -- The setup wizard's resume point (docs/ADMIN.md §4): 1 home/business, 2
    -- country, 3 numbers, 4 people, 5 phone line, 6 calling permissions, 7
    -- test call. The wizard's own data lives on the real resources it's
    -- filling in (this table, app_user, trunk, call_permission_level); this
    -- is only where to pick back up.
    ADD COLUMN setup_step        smallint NOT NULL DEFAULT 1 CHECK (setup_step BETWEEN 1 AND 7),
    ADD COLUMN setup_completed_at timestamptz;

-- "Confirm it's you" (docs/ADMIN.md §7): a session's most recent proof of
-- identity (sign-in, or POST /session/confirm), for actions that need one
-- within the last 10 minutes even in an already-signed-in session.
ALTER TABLE user_session ADD COLUMN confirmed_at timestamptz;
