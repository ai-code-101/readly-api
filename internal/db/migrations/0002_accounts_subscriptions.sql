-- Readers identified by their phone number (verified via SMS OTP).
CREATE TABLE users (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    phone          text        NOT NULL UNIQUE,      -- normalised 2547XXXXXXXX
    created_at     timestamptz NOT NULL DEFAULT now(),
    last_login_at  timestamptz
);

-- One row per OTP sent. Only an HMAC of the code is stored.
CREATE TABLE otp_requests (
    id              bigserial PRIMARY KEY,
    phone           text        NOT NULL,
    purpose         text        NOT NULL CHECK (purpose IN ('subscribe', 'login')),
    code_hash       text        NOT NULL,
    request_id      uuid        NOT NULL UNIQUE,     -- sent to the SMS service for idempotency
    sms_message_id  text,
    ip              text        NOT NULL DEFAULT '',
    attempts        int         NOT NULL DEFAULT 0,
    expires_at      timestamptz NOT NULL,
    used_at         timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX otp_requests_phone_idx ON otp_requests (phone, purpose, created_at DESC);
CREATE INDEX otp_requests_ip_idx ON otp_requests (ip, created_at DESC);

-- Login sessions. Only a SHA-256 of the session token is stored.
CREATE TABLE sessions (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id       uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash    text        NOT NULL UNIQUE,
    created_at    timestamptz NOT NULL DEFAULT now(),
    expires_at    timestamptz NOT NULL,
    last_seen_at  timestamptz NOT NULL DEFAULT now()
);

-- Every paid (or, for now, granted) period is its own row; a user is
-- subscribed while now() falls inside any [starts_at, ends_at).
CREATE TABLE subscriptions (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    plan        text        NOT NULL DEFAULT 'daily_airtime',
    amount_kes  int         NOT NULL DEFAULT 10,
    source      text        NOT NULL DEFAULT 'otp',   -- 'otp' until airtime billing exists; later 'airtime', 'admin', ...
    starts_at   timestamptz NOT NULL,
    ends_at     timestamptz NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    CHECK (ends_at > starts_at)
);
CREATE INDEX subscriptions_user_idx ON subscriptions (user_id, ends_at DESC);
CREATE INDEX subscriptions_period_idx ON subscriptions (starts_at, ends_at);

-- Where a signed-in reader stopped in each book.
CREATE TABLE reading_progress (
    user_id     uuid          NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    book_id     uuid          NOT NULL REFERENCES books(id) ON DELETE CASCADE,
    cfi         text          NOT NULL,
    percent     numeric(5,4)  NOT NULL DEFAULT 0 CHECK (percent >= 0 AND percent <= 1),
    chapter     text          NOT NULL DEFAULT '',
    updated_at  timestamptz   NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, book_id)
);

-- Trending is now set per day (Africa/Nairobi) and drops off automatically the next day.
ALTER TABLE books ADD COLUMN trending_on date;
UPDATE books SET trending_on = (now() AT TIME ZONE 'Africa/Nairobi')::date WHERE is_trending;
ALTER TABLE books DROP COLUMN is_trending;
CREATE INDEX books_trending_on_idx ON books (trending_on) WHERE trending_on IS NOT NULL;
