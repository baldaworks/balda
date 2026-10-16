-- +goose Up
CREATE TABLE balda_archive_final_members (
 archive_id TEXT NOT NULL REFERENCES balda_archive_sessions(archive_id),
 member_key TEXT NOT NULL,
 source_key TEXT NOT NULL REFERENCES balda_archive_messages(source_key),
 PRIMARY KEY (archive_id, member_key)
);

-- +goose Down
DROP TABLE balda_archive_final_members;
