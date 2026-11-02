CREATE TABLE drops (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    title          text        NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
    total_qty      int         NOT NULL CHECK (total_qty > 0),
    remaining      int         NOT NULL CHECK (remaining >= 0),
    price_idr      bigint      NOT NULL CHECK (price_idr >= 0),
    max_per_buyer  int         NOT NULL DEFAULT 1 CHECK (max_per_buyer > 0),
    hold_seconds   int         NOT NULL DEFAULT 600 CHECK (hold_seconds > 0),
    opens_at       timestamptz NOT NULL,
    closes_at      timestamptz NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    CHECK (remaining <= total_qty),
    CHECK (closes_at > opens_at)
);

CREATE TABLE claims (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    drop_id          uuid        NOT NULL REFERENCES drops (id),
    buyer_ref        text        NOT NULL CHECK (length(buyer_ref) BETWEEN 1 AND 200),
    qty              int         NOT NULL CHECK (qty > 0),
    status           text        NOT NULL CHECK (status IN ('HELD', 'CONFIRMED', 'EXPIRED', 'RELEASED')),
    expires_at       timestamptz NOT NULL,
    idempotency_key  text        NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 200),
    created_at       timestamptz NOT NULL DEFAULT now(),
    confirmed_at     timestamptz,
    UNIQUE (drop_id, idempotency_key)
);

-- one active claim per buyer per drop
CREATE UNIQUE INDEX claims_one_active_per_buyer
    ON claims (drop_id, buyer_ref) WHERE status IN ('HELD', 'CONFIRMED');

-- lets the expiry worker find overdue holds without scanning the table
CREATE INDEX claims_held_expiry ON claims (expires_at) WHERE status = 'HELD';
