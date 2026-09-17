-- +goose Up
-- =============================================================================
--  Baseline 8/9 · webhooks (outbound to merchants, inbound from providers)
-- =============================================================================

CREATE TABLE webhook_endpoints (
    id              UUID        PRIMARY KEY DEFAULT uuidv7(),
    organization_id UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    url             TEXT        NOT NULL,
    secret_enc      BYTEA       NOT NULL,                    -- HMAC signing secret, encrypted by the app
    event_types     TEXT[]      NOT NULL DEFAULT '{*}',
    description     TEXT,
    is_active       BOOLEAN     NOT NULL DEFAULT TRUE,
    created_by      UUID        REFERENCES users(id),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- TLS only: payloads carry payment data. Private / link-local targets (SSRF) are rejected by the app.
    CONSTRAINT webhook_endpoints_https CHECK (url ~* '^https://[^/[:space:]]+')
);
CREATE TRIGGER trg_webhook_endpoints_updated BEFORE UPDATE ON webhook_endpoints FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE INDEX webhook_endpoints_org_idx ON webhook_endpoints(organization_id) WHERE is_active;
GRANT SELECT, INSERT, UPDATE, DELETE ON webhook_endpoints TO templ_app;

-- Business events, fan-out source for deliveries. Append-only for the app.
CREATE TABLE events (
    id              UUID        PRIMARY KEY DEFAULT uuidv7(),
    organization_id UUID        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    type            TEXT        NOT NULL,                    -- 'invoice.paid', 'withdrawal.completed', ...
    resource_type   TEXT        NOT NULL,
    resource_id     UUID        NOT NULL,
    payload         JSONB       NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX events_org_created_idx ON events(organization_id, created_at DESC);
CREATE INDEX events_resource_idx    ON events(resource_type, resource_id);
CREATE INDEX events_created_brin    ON events USING BRIN (created_at);       -- retention
GRANT SELECT, INSERT ON events TO templ_app;

CREATE TABLE webhook_deliveries (
    id               UUID                    PRIMARY KEY DEFAULT uuidv7(),
    endpoint_id      UUID                    NOT NULL REFERENCES webhook_endpoints(id) ON DELETE CASCADE,
    event_id         UUID                    NOT NULL REFERENCES events(id)            ON DELETE CASCADE,
    status           webhook_delivery_status NOT NULL DEFAULT 'pending',
    attempts         INT                     NOT NULL DEFAULT 0,
    max_attempts     INT                     NOT NULL DEFAULT 10,
    next_attempt_at  TIMESTAMPTZ             NOT NULL DEFAULT now(),
    last_attempt_at  TIMESTAMPTZ,
    last_status_code SMALLINT,
    last_error       TEXT,
    delivered_at     TIMESTAMPTZ,
    created_at       TIMESTAMPTZ             NOT NULL DEFAULT now(),
    UNIQUE (endpoint_id, event_id),
    CONSTRAINT webhook_deliveries_attempts CHECK (max_attempts > 0 AND attempts >= 0)
);
CREATE INDEX webhook_deliveries_due_idx ON webhook_deliveries(next_attempt_at) WHERE status IN ('pending', 'failed');
GRANT SELECT, INSERT, UPDATE ON webhook_deliveries TO templ_app;

-- Raw callbacks received from custodial / exchange providers, stored before processing.
CREATE TABLE provider_webhook_events (
    id                BIGINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    provider_id       SMALLINT    NOT NULL REFERENCES payment_providers(id),
    external_event_id TEXT,
    event_type        TEXT,
    payload           JSONB       NOT NULL,
    signature_valid   BOOLEAN,
    received_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    processed_at      TIMESTAMPTZ,
    error             TEXT
);
CREATE UNIQUE INDEX provider_webhook_events_dedupe_uq
    ON provider_webhook_events(provider_id, external_event_id) WHERE external_event_id IS NOT NULL;
CREATE INDEX provider_webhook_events_unprocessed_idx ON provider_webhook_events(received_at) WHERE processed_at IS NULL;
CREATE INDEX provider_webhook_events_received_brin   ON provider_webhook_events USING BRIN (received_at);
GRANT SELECT, INSERT, UPDATE ON provider_webhook_events TO templ_app;

-- +goose Down
DROP TABLE provider_webhook_events;
DROP TABLE webhook_deliveries;
DROP TABLE events;
DROP TABLE webhook_endpoints;
