-- pgdu sample data
--
-- Builds a throwaway database with a variety of relations for exercising
-- pgdu's views: heap-heavy tables, index-heavy tables (btree, partial, GIN
-- trigram, GIN jsonb), out-of-line TOAST columns, and a deliberately bloated
-- table. The large text/bytea columns use high-entropy data so they actually
-- spill into TOAST rather than compressing inline.
--
-- Usage:
--   createdb pgdu_test
--   psql -d pgdu_test -f docs/sample-data.sql
--   pgdu -d pgdu_test
--
\set ON_ERROR_STOP on

-- Extensions used for richer data / index types
CREATE EXTENSION IF NOT EXISTS pgcrypto;   -- gen_random_bytes for bytea TOAST
CREATE EXTENSION IF NOT EXISTS pg_trgm;    -- GIN trigram index

----------------------------------------------------------------------
-- Schemas
----------------------------------------------------------------------
CREATE SCHEMA IF NOT EXISTS app;
CREATE SCHEMA IF NOT EXISTS analytics;
CREATE SCHEMA IF NOT EXISTS archive;

----------------------------------------------------------------------
-- app.users : narrow table, lots of rows, several btree indexes
----------------------------------------------------------------------
DROP TABLE IF EXISTS app.users CASCADE;
CREATE TABLE app.users (
    id          bigserial PRIMARY KEY,
    username    text NOT NULL,
    email       text NOT NULL,
    country     text,
    created_at  timestamptz NOT NULL,
    is_active   boolean NOT NULL,
    balance     numeric(12,2)
);

INSERT INTO app.users (username, email, country, created_at, is_active, balance)
SELECT
    'user_' || g,
    'user_' || g || '@example.com',
    (ARRAY['DE','US','FR','JP','BR','IN','GB','CA'])[1 + (random()*7)::int],
    now() - ((random()*3650)::int || ' days')::interval,
    random() < 0.85,
    round((random()*10000)::numeric, 2)
FROM generate_series(1, 80000) g;

CREATE UNIQUE INDEX users_email_key ON app.users (email);
CREATE INDEX users_country_idx   ON app.users (country);
CREATE INDEX users_created_idx   ON app.users (created_at);
CREATE INDEX users_active_idx    ON app.users (is_active) WHERE is_active;

----------------------------------------------------------------------
-- app.documents : heavy TOAST (large text + bytea stored out-of-line)
----------------------------------------------------------------------
DROP TABLE IF EXISTS app.documents CASCADE;
CREATE TABLE app.documents (
    id          bigserial PRIMARY KEY,
    owner_id    bigint REFERENCES app.users(id),
    title       text NOT NULL,
    body        text NOT NULL,        -- multi-KB → TOASTed
    payload     bytea,                -- random bytes → TOASTed
    tags        text[],
    updated_at  timestamptz DEFAULT now()
);

INSERT INTO app.documents (owner_id, title, body, payload, tags)
SELECT
    1 + (random()*79999)::int,
    'Document #' || g,
    -- high-entropy text (~3-9 KB) so it does NOT compress and spills into TOAST
    (SELECT string_agg(md5(random()::text), '') FROM generate_series(1, 100 + (random()*180)::int)),
    convert_to((SELECT string_agg(md5(random()::text), '') FROM generate_series(1, 80 + (random()*120)::int)), 'UTF8'),
    ARRAY['tag' || (random()*20)::int, 'tag' || (random()*20)::int]
FROM generate_series(1, 12000) g;

CREATE INDEX documents_owner_idx ON app.documents (owner_id);
-- GIN trigram index on the big text column
CREATE INDEX documents_body_trgm ON app.documents USING gin (body gin_trgm_ops);
-- GIN index on the array column
CREATE INDEX documents_tags_gin  ON app.documents USING gin (tags);

----------------------------------------------------------------------
-- app.events : large row count, jsonb (TOASTable), GIN on jsonb
----------------------------------------------------------------------
DROP TABLE IF EXISTS app.events CASCADE;
CREATE TABLE app.events (
    id          bigserial PRIMARY KEY,
    user_id     bigint,
    event_type  text NOT NULL,
    payload     jsonb,
    occurred_at timestamptz NOT NULL
);

INSERT INTO app.events (user_id, event_type, payload, occurred_at)
SELECT
    1 + (random()*79999)::int,
    (ARRAY['login','logout','click','purchase','view','error'])[1 + (random()*5)::int],
    jsonb_build_object(
        'ip', (random()*255)::int || '.' || (random()*255)::int || '.' || (random()*255)::int || '.' || (random()*255)::int,
        'agent', md5(random()::text),
        'meta', repeat(md5(random()::text), 5)
    ),
    now() - ((random()*365)::int || ' days')::interval
FROM generate_series(1, 250000) g;

CREATE INDEX events_user_idx    ON app.events (user_id);
CREATE INDEX events_type_idx    ON app.events (event_type);
CREATE INDEX events_time_idx    ON app.events (occurred_at);
CREATE INDEX events_payload_gin ON app.events USING gin (payload);

----------------------------------------------------------------------
-- analytics.metrics : wide-ish numeric table, no TOAST
----------------------------------------------------------------------
DROP TABLE IF EXISTS analytics.metrics CASCADE;
CREATE TABLE analytics.metrics (
    id        bigserial PRIMARY KEY,
    host      text NOT NULL,
    metric    text NOT NULL,
    value     double precision NOT NULL,
    ts        timestamptz NOT NULL
);

INSERT INTO analytics.metrics (host, metric, value, ts)
SELECT
    'host-' || (random()*50)::int,
    (ARRAY['cpu','mem','disk','net_in','net_out','load'])[1 + (random()*5)::int],
    random()*100,
    now() - ((random()*90)::int || ' days')::interval
FROM generate_series(1, 400000) g;

CREATE INDEX metrics_host_ts_idx ON analytics.metrics (host, ts);
CREATE INDEX metrics_metric_idx  ON analytics.metrics (metric);

----------------------------------------------------------------------
-- analytics.logs : text-heavy, some bloat from updates/deletes
----------------------------------------------------------------------
DROP TABLE IF EXISTS analytics.logs CASCADE;
CREATE TABLE analytics.logs (
    id       bigserial PRIMARY KEY,
    level    text NOT NULL,
    message  text NOT NULL,
    logged_at timestamptz NOT NULL
);

INSERT INTO analytics.logs (level, message, logged_at)
SELECT
    (ARRAY['DEBUG','INFO','WARN','ERROR'])[1 + (random()*3)::int],
    'log line ' || g || ' ' || md5(random()::text) || ' ' || md5(random()::text),
    now() - ((random()*30)::int || ' days')::interval
FROM generate_series(1, 150000) g;

CREATE INDEX logs_level_idx ON analytics.logs (level);

-- Create bloat: churn rows so dead tuples accumulate (good for the bloat view)
UPDATE analytics.logs SET message = message || ' (edited)' WHERE id % 3 = 0;
DELETE FROM analytics.logs WHERE id % 7 = 0;

----------------------------------------------------------------------
-- archive.old_events : a partition-like cold table
----------------------------------------------------------------------
DROP TABLE IF EXISTS archive.old_events CASCADE;
CREATE TABLE archive.old_events (
    id          bigint,
    user_id     bigint,
    blob        text,
    archived_at timestamptz DEFAULT now()
);

INSERT INTO archive.old_events (id, user_id, blob)
SELECT
    g,
    1 + (random()*79999)::int,
    (SELECT string_agg(md5(random()::text), '') FROM generate_series(1, 150))   -- high-entropy → TOASTed
FROM generate_series(1, 8000) g;

----------------------------------------------------------------------
-- Update planner stats
----------------------------------------------------------------------
ANALYZE;
