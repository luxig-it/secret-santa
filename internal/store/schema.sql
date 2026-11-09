CREATE TABLE IF NOT EXISTS campaign (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    title           TEXT    NOT NULL,
    budget          TEXT    NOT NULL DEFAULT '',
    draw_date       TEXT    NOT NULL DEFAULT '',
    join_token      TEXT    NOT NULL UNIQUE,
    organizer_token TEXT    NOT NULL UNIQUE,
    organizer_label TEXT    NOT NULL DEFAULT '',
    state           TEXT    NOT NULL DEFAULT 'open',
    created_at      TEXT    NOT NULL
);

CREATE TABLE IF NOT EXISTS participant (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    campaign_id INTEGER NOT NULL REFERENCES campaign(id) ON DELETE CASCADE,
    name        TEXT    NOT NULL,
    email       TEXT    NOT NULL DEFAULT '',
    wishlist    TEXT    NOT NULL DEFAULT '',
    ready       INTEGER NOT NULL DEFAULT 0,
    token       TEXT    NOT NULL UNIQUE,
    created_at  TEXT    NOT NULL
);

CREATE INDEX IF NOT EXISTS participant_campaign ON participant(campaign_id);

-- An unordered pair of people who must not draw each other, stored with
-- a_id < b_id so the same pair cannot be entered twice.
CREATE TABLE IF NOT EXISTS exclusion (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    campaign_id INTEGER NOT NULL REFERENCES campaign(id) ON DELETE CASCADE,
    a_id        INTEGER NOT NULL REFERENCES participant(id) ON DELETE CASCADE,
    b_id        INTEGER NOT NULL REFERENCES participant(id) ON DELETE CASCADE,
    UNIQUE (campaign_id, a_id, b_id)
);

CREATE TABLE IF NOT EXISTS assignment (
    campaign_id INTEGER NOT NULL REFERENCES campaign(id) ON DELETE CASCADE,
    giver_id    INTEGER NOT NULL REFERENCES participant(id) ON DELETE CASCADE,
    receiver_id INTEGER NOT NULL REFERENCES participant(id) ON DELETE CASCADE,
    PRIMARY KEY (campaign_id, giver_id)
);
