CREATE TABLE repositories (
    repository_id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    module_path VARCHAR(512) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    UNIQUE KEY repositories_module_path_uq (module_path)
) ENGINE=InnoDB;

CREATE TABLE app_statistics (
    singleton_id SMALLINT PRIMARY KEY CHECK (singleton_id = 1),
    repository_count BIGINT NOT NULL DEFAULT 0 CHECK (repository_count >= 0)
) ENGINE=InnoDB;

INSERT INTO app_statistics (singleton_id, repository_count)
VALUES (1, 0)
ON DUPLICATE KEY UPDATE singleton_id = VALUES(singleton_id);

CREATE TABLE reports (
    repository_id BIGINT PRIMARY KEY,
    version TEXT NOT NULL,
    resolved_repo TEXT NOT NULL,
    average DOUBLE NOT NULL CHECK (average BETWEEN 0 AND 1),
    grade VARCHAR(2) NOT NULL CHECK (grade IN ('A+', 'A', 'B', 'C', 'D', 'E', 'F')),
    file_count INTEGER NOT NULL CHECK (file_count >= 0),
    issue_count INTEGER NOT NULL CHECK (issue_count >= 0),
    did_error BOOLEAN NOT NULL,
    refreshed_at DATETIME(6) NOT NULL,
    CONSTRAINT reports_repository_fk FOREIGN KEY (repository_id)
        REFERENCES repositories (repository_id) ON DELETE CASCADE,
    KEY reports_leaderboard_idx (average DESC, repository_id)
) ENGINE=InnoDB;

CREATE TABLE report_checks (
    repository_id BIGINT NOT NULL,
    check_ordinal INTEGER NOT NULL CHECK (check_ordinal >= 0),
    name TEXT NOT NULL,
    description TEXT NOT NULL,
    weight DOUBLE NOT NULL CHECK (weight >= 0),
    percentage DOUBLE NOT NULL CHECK (percentage BETWEEN 0 AND 1),
    error_message TEXT NOT NULL,
    PRIMARY KEY (repository_id, check_ordinal),
    CONSTRAINT report_checks_report_fk FOREIGN KEY (repository_id)
        REFERENCES reports (repository_id) ON DELETE CASCADE
) ENGINE=InnoDB;

CREATE TABLE report_check_files (
    repository_id BIGINT NOT NULL,
    check_ordinal INTEGER NOT NULL,
    file_ordinal INTEGER NOT NULL CHECK (file_ordinal >= 0),
    filename TEXT NOT NULL,
    file_url TEXT NOT NULL,
    PRIMARY KEY (repository_id, check_ordinal, file_ordinal),
    CONSTRAINT report_check_files_check_fk FOREIGN KEY (repository_id, check_ordinal)
        REFERENCES report_checks (repository_id, check_ordinal) ON DELETE CASCADE
) ENGINE=InnoDB;

CREATE TABLE report_check_errors (
    repository_id BIGINT NOT NULL,
    check_ordinal INTEGER NOT NULL,
    file_ordinal INTEGER NOT NULL,
    error_ordinal INTEGER NOT NULL CHECK (error_ordinal >= 0),
    line_number INTEGER NOT NULL,
    error_string TEXT NOT NULL,
    PRIMARY KEY (repository_id, check_ordinal, file_ordinal, error_ordinal),
    CONSTRAINT report_check_errors_file_fk FOREIGN KEY (repository_id, check_ordinal, file_ordinal)
        REFERENCES report_check_files (repository_id, check_ordinal, file_ordinal) ON DELETE CASCADE
) ENGINE=InnoDB;

CREATE TABLE recent_repositories (
    recent_id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
    repository_id BIGINT NOT NULL UNIQUE,
    CONSTRAINT recent_repositories_repository_fk FOREIGN KEY (repository_id)
        REFERENCES repositories (repository_id) ON DELETE CASCADE
) ENGINE=InnoDB;
