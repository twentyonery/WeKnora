-- Learning Navigator (topic 4): per-person knowledge-network mastery.
--
-- learning_node_events is the append-only behavior log (page views and other
-- direct exposures). learning_node_states is the derived, per-page mastery
-- snapshot the profile API reads. The mapping rows in
-- learning_page_source_index let document-level familiarity be attributed to
-- the wiki pages that cite those documents.
CREATE TABLE learning_node_events (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id BIGINT NOT NULL,
    subject_id VARCHAR(512) NOT NULL,
    knowledge_base_id VARCHAR(36) NOT NULL,
    slug VARCHAR(255) NOT NULL,
    signal_type VARCHAR(16) NOT NULL,
    weight DOUBLE PRECISION NOT NULL DEFAULT 1,
    occurred_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_learning_events_scope
    ON learning_node_events(tenant_id, subject_id, knowledge_base_id, slug);

CREATE TABLE learning_node_states (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id BIGINT NOT NULL,
    subject_id VARCHAR(512) NOT NULL,
    knowledge_base_id VARCHAR(36) NOT NULL,
    slug VARCHAR(255) NOT NULL,
    raw_score DOUBLE PRECISION NOT NULL DEFAULT 0,
    mastery DOUBLE PRECISION NOT NULL DEFAULT 0,
    state VARCHAR(16) NOT NULL DEFAULT 'dark',
    signal_doc DOUBLE PRECISION NOT NULL DEFAULT 0,
    signal_topic DOUBLE PRECISION NOT NULL DEFAULT 0,
    signal_view DOUBLE PRECISION NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_learning_state UNIQUE (tenant_id, subject_id, knowledge_base_id, slug)
);
CREATE INDEX idx_learning_states_scope
    ON learning_node_states(tenant_id, subject_id, knowledge_base_id);

CREATE TABLE learning_page_source_index (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id BIGINT NOT NULL,
    knowledge_base_id VARCHAR(36) NOT NULL,
    knowledge_id VARCHAR(36) NOT NULL,
    slug VARCHAR(255) NOT NULL,
    ref_weight DOUBLE PRECISION NOT NULL DEFAULT 1,
    CONSTRAINT uq_learning_page_source UNIQUE (knowledge_base_id, knowledge_id, slug)
);
CREATE INDEX idx_learning_page_source_kb
    ON learning_page_source_index(tenant_id, knowledge_base_id);
