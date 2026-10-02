-- +goose Up
-- The active generation determines the current family deadline. Historical
-- token expirations remain immutable for replay detection.
DROP TRIGGER trg_balda_backoffice_sessions_fixed_refresh_expiry;
