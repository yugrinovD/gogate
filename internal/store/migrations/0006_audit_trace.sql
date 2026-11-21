-- Correlate request audit records with OTel traces in Jaeger.
ALTER TABLE request_audit
    ADD COLUMN IF NOT EXISTS trace_id text NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS request_audit_trace_id_idx
    ON request_audit(trace_id)
    WHERE trace_id <> '';
