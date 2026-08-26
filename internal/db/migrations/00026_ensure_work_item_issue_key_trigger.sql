-- +goose Up
-- Keep issue keys populated even when an integration writes directly to the
-- work_items table instead of using the Brain API.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION stash_set_work_item_issue_key()
RETURNS trigger AS $$
BEGIN
    IF NEW.issue_key = '' THEN
        NEW.issue_key := 'W-' || lpad(NEW.id::text, 6, '0');
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS work_items_issue_key_trigger ON work_items;
CREATE TRIGGER work_items_issue_key_trigger
    BEFORE INSERT ON work_items
    FOR EACH ROW EXECUTE FUNCTION stash_set_work_item_issue_key();

-- +goose Down
-- The trigger is part of 00025's schema. Keep that invariant when this
-- repair migration is rolled back.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION stash_set_work_item_issue_key()
RETURNS trigger AS $$
BEGIN
    IF NEW.issue_key = '' THEN
        NEW.issue_key := 'W-' || lpad(NEW.id::text, 6, '0');
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS work_items_issue_key_trigger ON work_items;
CREATE TRIGGER work_items_issue_key_trigger
    BEFORE INSERT ON work_items
    FOR EACH ROW EXECUTE FUNCTION stash_set_work_item_issue_key();
