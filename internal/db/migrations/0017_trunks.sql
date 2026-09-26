-- Trunks: phone lines to providers and other phone systems (docs/TRUNKS.md,
-- ADR-043 to ADR-048). This migration is data and API only (1D step 2):
-- nothing here talks to Asterisk yet (that's step 3's pjsip_trunks.conf
-- render) and outbound routing doesn't pick a line yet (also step 3).
-- Passwords and WireGuard keys are sealed with dbsecret (ADR-030), never
-- stored in clear or in a view.

CREATE TABLE wireguard_profile (
    id                   uuid PRIMARY KEY,
    tenant_id            uuid NOT NULL REFERENCES tenant (id),
    name                 text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    -- This end's own tunnel address (e.g. "10.6.0.2/32").
    address              text NOT NULL CHECK (length(address) BETWEEN 1 AND 100),
    -- Row id "wireguard_profile:<id>".
    private_key_enc      bytea NOT NULL,
    -- Derived from the private key at import time (not secret); shown so
    -- the admin can hand it to a provider that needs it registered.
    public_key           text NOT NULL,
    peer_public_key      text NOT NULL,
    peer_endpoint_host   text NOT NULL CHECK (length(peer_endpoint_host) BETWEEN 1 AND 255),
    peer_endpoint_port   integer NOT NULL DEFAULT 51820 CHECK (peer_endpoint_port BETWEEN 1 AND 65535),
    -- Row id "wireguard_profile_psk:<id>"; NULL if the profile has none.
    preshared_key_enc    bytea,
    persistent_keepalive integer CHECK (persistent_keepalive > 0),
    version              integer NOT NULL DEFAULT 1,
    created_at           timestamptz NOT NULL,
    updated_at           timestamptz NOT NULL,
    UNIQUE (tenant_id, name)
);

-- Who may call what (docs/TRUNKS.md §5). Categories are numbering.Category
-- values; emergency and invalid are never gated, so they're never listed
-- here (numbering_route checks emergency before this table is even read).
CREATE TABLE call_permission_level (
    id                 uuid PRIMARY KEY,
    tenant_id          uuid NOT NULL REFERENCES tenant (id),
    name               text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    allowed_categories text[] NOT NULL DEFAULT '{}'
        CHECK (allowed_categories <@ ARRAY['landline', 'mobile', 'national', 'shared_cost',
                                            'toll_free', 'premium', 'international', 'service']::text[]),
    withhold_caller_id boolean NOT NULL DEFAULT false,
    version            integer NOT NULL DEFAULT 1,
    created_at         timestamptz NOT NULL,
    updated_at         timestamptz NOT NULL,
    UNIQUE (tenant_id, name)
);

-- An extension with no level assigned may only call emergency numbers
-- (today's behaviour, migration 0016): RESTRICT so deleting a level in use
-- can't silently strip everyone's calling permission.
ALTER TABLE extension ADD COLUMN call_permission_level_id uuid REFERENCES call_permission_level (id) ON DELETE RESTRICT;

CREATE TABLE trunk (
    id                       uuid PRIMARY KEY,
    tenant_id                uuid NOT NULL REFERENCES tenant (id),
    name                     text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    -- "registration": Linx signs in. "ip_authenticated": the provider calls
    -- in from its own addresses. "lan_peer": another phone system on the
    -- LAN (docs/TRUNKS.md §3).
    kind                     text NOT NULL CHECK (kind IN ('registration', 'ip_authenticated', 'lan_peer')),
    -- Which entry of the provider template catalogue (internal/trunk)
    -- this was created from, if any; informational only.
    template                 text NOT NULL DEFAULT '',
    host                     text NOT NULL CHECK (length(host) BETWEEN 1 AND 255),
    port                     integer NOT NULL DEFAULT 5061 CHECK (port BETWEEN 1 AND 65535),
    transport                text NOT NULL DEFAULT 'tls' CHECK (transport IN ('tls', 'tcp', 'udp')),
    media_encryption         text NOT NULL DEFAULT 'srtp' CHECK (media_encryption IN ('srtp', 'none')),
    -- "pinned": the admin approved a specific certificate or CA (ADR-045).
    cert_trust               text NOT NULL DEFAULT 'public' CHECK (cert_trust IN ('public', 'pinned')),
    pinned_certificate       text,
    username                 text NOT NULL DEFAULT '',
    -- Row id "trunk:<id>"; NULL for a trunk with no login of its own
    -- (an IP-authenticated or LAN-peer trunk may have none).
    password_enc             bytea,
    -- How outgoing numbers are written for this trunk.
    dial_format              text NOT NULL DEFAULT 'e164' CHECK (dial_format IN ('e164', '00_prefix', 'local')),
    codecs                   text[] NOT NULL DEFAULT '{alaw,ulaw}'
        CHECK (codecs <@ ARRAY['ulaw', 'alaw', 'g722', 'opus']::text[] AND cardinality(codecs) > 0),
    caller_id_number         text,
    max_calls                integer NOT NULL DEFAULT 4 CHECK (max_calls BETWEEN 1 AND 500),
    -- NULL: "Internet" (ADR-024); otherwise the WireGuard profile it's
    -- reached through. RESTRICT: detach trunks before deleting a profile.
    wireguard_profile_id     uuid REFERENCES wireguard_profile (id) ON DELETE RESTRICT,
    -- 1 = tried first, then 2, and so on; NULL = not used for outgoing
    -- calls at all (set only through the outbound-routing reorder, so two
    -- trunks can never tie).
    outbound_priority        integer CHECK (outbound_priority > 0),
    -- Who confirmed the ADR-023 "this trunk is unencrypted" warning, and
    -- when; cleared whenever the trunk isn't unencrypted, so it must be
    -- confirmed again if it becomes unencrypted later.
    unencrypted_confirmed_by text,
    unencrypted_confirmed_at timestamptz,
    enabled                  boolean NOT NULL DEFAULT true,
    version                  integer NOT NULL DEFAULT 1,
    created_at               timestamptz NOT NULL,
    updated_at               timestamptz NOT NULL,
    UNIQUE (tenant_id, name),
    UNIQUE (tenant_id, outbound_priority)
);

-- A phone number a trunk owns (docs/TRUNKS.md §5). extension_id NULL: a
-- call to it hears "not in use" (dialplan work is step 3).
CREATE TABLE trunk_did (
    id           uuid PRIMARY KEY,
    tenant_id    uuid NOT NULL REFERENCES tenant (id),
    trunk_id     uuid NOT NULL REFERENCES trunk (id) ON DELETE CASCADE,
    number       text NOT NULL CHECK (number ~ '^\+?[0-9]{2,20}$'),
    label        text NOT NULL DEFAULT '' CHECK (length(label) <= 100),
    extension_id uuid REFERENCES extension (id) ON DELETE SET NULL,
    version      integer NOT NULL DEFAULT 1,
    created_at   timestamptz NOT NULL,
    updated_at   timestamptz NOT NULL,
    -- A DID can't be claimed by two trunks (docs/TRUNKS.md §5).
    UNIQUE (tenant_id, number)
);

CREATE INDEX trunk_did_trunk_idx ON trunk_did (trunk_id, id DESC);
CREATE INDEX trunk_tenant_idx ON trunk (tenant_id, id DESC);
CREATE INDEX wireguard_profile_tenant_idx ON wireguard_profile (tenant_id, id DESC);
CREATE INDEX call_permission_level_tenant_idx ON call_permission_level (tenant_id, id DESC);

-- numbering_route (migration 0016) gains permission levels: a category
-- other than emergency now needs the caller's level to allow it, or the
-- reason is 'not_permitted' instead of 'no_lines'. Picking an actual line
-- (steps 2-3, docs/TRUNKS.md §5) is step 3's job, once the dialplan that
-- consumes it exists; until then an allowed call still comes back
-- 'no_lines'. The OUT signature is unchanged, so asterisk.linx_route_outbound
-- needs no change.
CREATE OR REPLACE FUNCTION numbering_route(caller uuid, dialled text,
    OUT category text, OUT number_type text, OUT region text, OUT e164 text, OUT dial text, OUT label text,
    OUT allowed boolean, OUT reason text)
LANGUAGE plpgsql STABLE AS $$
DECLARE
    caller_categories text[];
BEGIN
    SELECT c.* INTO category, number_type, region, e164, dial, label
    FROM pbx_setting s, numbering_classify(s.country, dialled) c;
    allowed := false;
    IF NOT EXISTS (SELECT 1 FROM extension e WHERE e.id = caller AND e.enabled AND e.deleted_at IS NULL) THEN
        reason := 'unknown_caller';
    ELSIF category = 'emergency' THEN
        allowed := true;
        reason := 'emergency';
    ELSIF category = 'invalid' THEN
        reason := 'invalid';
    ELSE
        SELECT l.allowed_categories INTO caller_categories
        FROM extension e JOIN call_permission_level l ON l.id = e.call_permission_level_id
        WHERE e.id = caller;
        IF caller_categories IS NULL OR NOT (category = ANY (caller_categories)) THEN
            reason := 'not_permitted';
        ELSE
            reason := 'no_lines';
        END IF;
    END IF;
END $$;
