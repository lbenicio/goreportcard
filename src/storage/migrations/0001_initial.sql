CREATE TABLE repositories (
    repository_id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    module_path TEXT NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE app_statistics (
    singleton_id SMALLINT PRIMARY KEY CHECK (singleton_id = 1),
    repository_count BIGINT NOT NULL DEFAULT 0 CHECK (repository_count >= 0)
);

INSERT INTO app_statistics (singleton_id, repository_count) VALUES (1, 0);

CREATE TABLE reports (
    repository_id BIGINT PRIMARY KEY
        REFERENCES repositories (repository_id) ON DELETE CASCADE,
    version TEXT NOT NULL,
    resolved_repo TEXT NOT NULL,
    average DOUBLE PRECISION NOT NULL CHECK (average BETWEEN 0 AND 1),
    grade VARCHAR(2) NOT NULL CHECK (grade IN ('A+', 'A', 'B', 'C', 'D', 'E', 'F')),
    file_count INTEGER NOT NULL CHECK (file_count >= 0),
    issue_count INTEGER NOT NULL CHECK (issue_count >= 0),
    did_error BOOLEAN NOT NULL,
    refreshed_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE report_checks (
    repository_id BIGINT NOT NULL
        REFERENCES reports (repository_id) ON DELETE CASCADE,
    check_ordinal INTEGER NOT NULL CHECK (check_ordinal >= 0),
    name TEXT NOT NULL,
    description TEXT NOT NULL,
    weight DOUBLE PRECISION NOT NULL CHECK (weight >= 0),
    percentage DOUBLE PRECISION NOT NULL CHECK (percentage BETWEEN 0 AND 1),
    error_message TEXT NOT NULL,
    PRIMARY KEY (repository_id, check_ordinal)
);

CREATE TABLE report_check_files (
    repository_id BIGINT NOT NULL,
    check_ordinal INTEGER NOT NULL,
    file_ordinal INTEGER NOT NULL CHECK (file_ordinal >= 0),
    filename TEXT NOT NULL,
    file_url TEXT NOT NULL,
    PRIMARY KEY (repository_id, check_ordinal, file_ordinal),
    FOREIGN KEY (repository_id, check_ordinal)
        REFERENCES report_checks (repository_id, check_ordinal) ON DELETE CASCADE
);

CREATE TABLE report_check_errors (
    repository_id BIGINT NOT NULL,
    check_ordinal INTEGER NOT NULL,
    file_ordinal INTEGER NOT NULL,
    error_ordinal INTEGER NOT NULL CHECK (error_ordinal >= 0),
    line_number INTEGER NOT NULL,
    error_string TEXT NOT NULL,
    PRIMARY KEY (repository_id, check_ordinal, file_ordinal, error_ordinal),
    FOREIGN KEY (repository_id, check_ordinal, file_ordinal)
        REFERENCES report_check_files (repository_id, check_ordinal, file_ordinal)
        ON DELETE CASCADE
);

CREATE TABLE recent_repositories (
    recent_id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    repository_id BIGINT NOT NULL UNIQUE
        REFERENCES repositories (repository_id) ON DELETE CASCADE
);

CREATE INDEX reports_leaderboard_idx
    ON reports (average DESC, repository_id)
    WHERE file_count >= 100;
